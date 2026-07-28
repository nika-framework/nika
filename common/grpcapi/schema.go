package grpcapi

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// timestampMessage is the well-known type a Go time.Time maps onto.
const timestampMessage = ".google.protobuf.Timestamp"

// timestampProto is the file a message must import to use it.
const timestampProto = "google/protobuf/timestamp.proto"

// timeType is compared against directly, before the generic struct walk, because
// time.Time is a struct whose fields are unexported and meaningless on the wire.
var timeType = reflect.TypeOf(time.Time{})

// bytesType is []byte, which must map to TYPE_BYTES rather than repeated uint8.
var bytesType = reflect.TypeOf([]byte(nil))

// fieldBinding records how one protobuf field maps back onto a Go struct field.
//
// The mapping is computed once while the descriptor is built and reused for every
// request. Recomputing it per call — or bridging through JSON — would mean paying
// reflection or a double serialisation on the hot path, and JSON in particular
// cannot round-trip an int64: protojson encodes it as a quoted string, which Go's
// encoding/json then refuses to read back into an int64 field.
type fieldBinding struct {
	number     protoreflect.FieldNumber
	name       string
	goIndex    int
	goType     reflect.Type
	kind       protoreflect.Kind
	repeated   bool
	optional   bool
	isTime     bool
	isBytes    bool
	nested     *messageBinding
	elemBind   *fieldBinding // for repeated fields, the element's binding
	descriptor protoreflect.FieldDescriptor
}

// messageBinding ties a Go struct type to the protobuf message derived from it.
type messageBinding struct {
	goType     reflect.Type
	name       string // fully qualified, e.g. "nika.user.v1.CreateUserRequest"
	fields     []*fieldBinding
	descriptor protoreflect.MessageDescriptor
}

// schemaBuilder derives protobuf messages from Go types.
//
// It exists so a service can be declared in Go and still be a real protobuf
// contract: the descriptors it produces are registered and served over gRPC
// reflection, so a Python, NestJS or Bruno client discovers the schema the normal
// way and speaks ordinary protobuf on the wire. There is no .proto file and no
// codegen step, but there is a genuine descriptor — which is what interoperability
// actually requires.
type schemaBuilder struct {
	pkg string

	// byType dedupes: two methods taking the same Go struct share one message.
	byType map[reflect.Type]*messageBinding

	// byName catches two different Go types deriving the same message name, which
	// would otherwise produce a file that fails to build with a confusing error.
	byName map[string]reflect.Type

	messages     []*descriptorpb.DescriptorProto
	needsTime    bool
	orderedBinds []*messageBinding
}

func newSchemaBuilder(pkg string) *schemaBuilder {
	return &schemaBuilder{
		pkg:    pkg,
		byType: make(map[reflect.Type]*messageBinding),
		byName: make(map[string]reflect.Type),
	}
}

// message returns the binding for a Go struct type, building it on first sight.
func (b *schemaBuilder) message(goType reflect.Type) (*messageBinding, error) {
	goType = deref(goType)

	if existing, ok := b.byType[goType]; ok {
		return existing, nil
	}
	if goType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("grpcapi: %s is not a struct; a request and a response must both be structs", goType)
	}
	if goType == timeType {
		return nil, fmt.Errorf("grpcapi: time.Time cannot be a top-level message; wrap it in a struct")
	}

	name := goType.Name()
	if name == "" {
		return nil, fmt.Errorf("grpcapi: anonymous struct types cannot be messages; give %s a name", goType)
	}
	qualified := b.pkg + "." + name

	if previous, clash := b.byName[qualified]; clash && previous != goType {
		return nil, fmt.Errorf(
			"grpcapi: %s and %s both derive the message %q; rename one, or put them in different services",
			previous, goType, qualified,
		)
	}
	b.byName[qualified] = goType

	binding := &messageBinding{goType: goType, name: qualified}
	// Register before walking the fields so a struct that refers to itself does
	// not recurse forever.
	b.byType[goType] = binding

	proto := &descriptorpb.DescriptorProto{Name: strPtr(name)}

	nextNumber := int32(1)
	for i := 0; i < goType.NumField(); i++ {
		structField := goType.Field(i)
		if !structField.IsExported() {
			continue
		}
		if structField.Tag.Get("grpc") == "-" || structField.Tag.Get("json") == "-" {
			// An explicitly excluded field: the canonical way to keep a password
			// hash out of a response is to leave it out of the contract.
			continue
		}

		field, err := b.field(structField, i, &nextNumber)
		if err != nil {
			return nil, fmt.Errorf("grpcapi: %s.%s: %w", goType.Name(), structField.Name, err)
		}
		if field == nil {
			continue
		}

		binding.fields = append(binding.fields, field)
		proto.Field = append(proto.Field, fieldProto(field))
	}

	if len(binding.fields) == 0 {
		return nil, fmt.Errorf("grpcapi: %s has no usable exported fields", goType)
	}

	b.messages = append(b.messages, proto)
	b.orderedBinds = append(b.orderedBinds, binding)
	return binding, nil
}

// field derives one protobuf field from a Go struct field.
func (b *schemaBuilder) field(structField reflect.StructField, index int, nextNumber *int32) (*fieldBinding, error) {
	number, err := fieldNumber(structField, nextNumber)
	if err != nil {
		return nil, err
	}

	binding := &fieldBinding{
		number:  protoreflect.FieldNumber(number),
		name:    fieldName(structField),
		goIndex: index,
		goType:  structField.Type,
	}

	fieldType := structField.Type

	// A pointer means proto3 optional: presence is tracked, so "absent" and "set
	// to the zero value" stay distinguishable. That distinction is the whole
	// reason a PATCH-shaped request needs pointers.
	if fieldType.Kind() == reflect.Ptr && fieldType != bytesType {
		binding.optional = true
		fieldType = fieldType.Elem()
	}

	// []byte is bytes, not a repeated field.
	if fieldType == bytesType {
		binding.kind = protoreflect.BytesKind
		binding.isBytes = true
		return binding, nil
	}

	if fieldType.Kind() == reflect.Slice {
		if binding.optional {
			return nil, fmt.Errorf("a pointer to a slice is not representable; use a plain slice")
		}
		binding.repeated = true
		element := fieldType.Elem()

		elemBinding, err := b.scalarOrMessage(element)
		if err != nil {
			return nil, err
		}
		binding.kind = elemBinding.kind
		binding.isTime = elemBinding.isTime
		binding.isBytes = elemBinding.isBytes
		binding.nested = elemBinding.nested
		binding.elemBind = elemBinding
		binding.goType = element
		return binding, nil
	}

	resolved, err := b.scalarOrMessage(fieldType)
	if err != nil {
		return nil, err
	}
	binding.kind = resolved.kind
	binding.isTime = resolved.isTime
	binding.isBytes = resolved.isBytes
	binding.nested = resolved.nested
	binding.goType = fieldType
	return binding, nil
}

// scalarOrMessage resolves a single (non-repeated, non-pointer) Go type.
func (b *schemaBuilder) scalarOrMessage(goType reflect.Type) (*fieldBinding, error) {
	goType = deref(goType)
	out := &fieldBinding{goType: goType}

	if goType == timeType {
		b.needsTime = true
		out.kind = protoreflect.MessageKind
		out.isTime = true
		return out, nil
	}
	if goType == bytesType {
		out.kind = protoreflect.BytesKind
		out.isBytes = true
		return out, nil
	}

	switch goType.Kind() {
	case reflect.String:
		out.kind = protoreflect.StringKind
	case reflect.Bool:
		out.kind = protoreflect.BoolKind
	case reflect.Int, reflect.Int64:
		out.kind = protoreflect.Int64Kind
	case reflect.Int8, reflect.Int16, reflect.Int32:
		out.kind = protoreflect.Int32Kind
	case reflect.Uint, reflect.Uint64:
		out.kind = protoreflect.Uint64Kind
	case reflect.Uint8, reflect.Uint16, reflect.Uint32:
		out.kind = protoreflect.Uint32Kind
	case reflect.Float32:
		out.kind = protoreflect.FloatKind
	case reflect.Float64:
		out.kind = protoreflect.DoubleKind
	case reflect.Struct:
		nested, err := b.message(goType)
		if err != nil {
			return nil, err
		}
		out.kind = protoreflect.MessageKind
		out.nested = nested
	case reflect.Map:
		// Deliberately unsupported for now rather than silently mapped to
		// something lossy: protobuf maps need a synthesised entry message and
		// have their own key-type restrictions.
		return nil, fmt.Errorf("map fields are not supported yet; use a repeated struct of key/value pairs")
	case reflect.Interface:
		return nil, fmt.Errorf("interface fields have no protobuf equivalent; use a concrete type")
	default:
		return nil, fmt.Errorf("%s has no protobuf equivalent", goType)
	}
	return out, nil
}

// fieldProto renders the descriptor entry for a field.
func fieldProto(f *fieldBinding) *descriptorpb.FieldDescriptorProto {
	label := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	if f.repeated {
		label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	}

	proto := &descriptorpb.FieldDescriptorProto{
		Name:     strPtr(f.name),
		JsonName: strPtr(jsonName(f.name)),
		Number:   int32Ptr(int32(f.number)),
		Label:    &label,
		Type:     kindToType(f.kind).Enum(),
	}

	switch {
	case f.isTime:
		proto.TypeName = strPtr(timestampMessage)
	case f.nested != nil:
		proto.TypeName = strPtr("." + f.nested.name)
	}

	// proto3 optional is expressed as a synthetic one-of; the index is filled in
	// when the message is assembled, because it must be unique per message.
	if f.optional && !f.repeated {
		proto.Proto3Optional = boolPtr(true)
	}

	return proto
}

// finalizeOptional assigns the synthetic one-of each proto3 optional field needs.
//
// The descriptor format expresses optional presence as a one-of containing a
// single field, and protodesc rejects a file where Proto3Optional is set without
// one. Doing it here, after every field is known, keeps the indices contiguous.
func finalizeOptional(message *descriptorpb.DescriptorProto) {
	for _, field := range message.Field {
		if field.GetProto3Optional() {
			index := int32(len(message.OneofDecl))
			message.OneofDecl = append(message.OneofDecl, &descriptorpb.OneofDescriptorProto{
				Name: strPtr("_" + field.GetName()),
			})
			field.OneofIndex = int32Ptr(index)
		}
	}
}

// fieldNumber resolves the wire number for a field.
//
// Numbers come from declaration order unless a `protobuf:"N"` tag pins them.
// Order-derived numbers are convenient and dangerous in equal measure: reordering
// two struct fields silently changes the wire format for every client already
// generated against it. Pin them explicitly on anything published.
func fieldNumber(structField reflect.StructField, next *int32) (int32, error) {
	tag := structField.Tag.Get("protobuf")
	if tag == "" {
		number := *next
		*next++
		return number, nil
	}

	parsed, err := strconv.Atoi(strings.TrimSpace(tag))
	if err != nil {
		return 0, fmt.Errorf("protobuf tag %q is not a number", tag)
	}
	number := int32(parsed)
	if number < 1 || number > 536870911 {
		return 0, fmt.Errorf("protobuf field number %d is out of range", number)
	}
	if number >= 19000 && number <= 19999 {
		return 0, fmt.Errorf("protobuf field number %d is in the reserved 19000-19999 range", number)
	}

	if number >= *next {
		*next = number + 1
	}
	return int32(number), nil
}

// fieldName picks the wire name: the json tag when present, else snake_case.
//
// Reusing the json tag means one struct describes both the REST body and the
// protobuf message, and a client sees the same field names on both.
func fieldName(structField reflect.StructField) string {
	if tag := structField.Tag.Get("json"); tag != "" {
		if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
			return name
		}
	}
	return snakeCase(structField.Name)
}

// snakeCase converts a Go field name to protobuf convention.
func snakeCase(name string) string {
	var out strings.Builder
	runes := []rune(name)

	for i, r := range runes {
		if unicode.IsUpper(r) {
			// Insert a separator at a lower→upper boundary, and at the end of an
			// acronym run (HTTPServer → http_server, not h_t_t_p_server).
			prevLower := i > 0 && unicode.IsLower(runes[i-1])
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if i > 0 && (prevLower || nextLower) {
				out.WriteByte('_')
			}
			out.WriteRune(unicode.ToLower(r))
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// jsonName is the lowerCamelCase form protobuf tooling expects alongside the
// proto name.
func jsonName(protoName string) string {
	parts := strings.Split(protoName, "_")
	var out strings.Builder
	for i, part := range parts {
		if part == "" {
			continue
		}
		if i == 0 {
			out.WriteString(part)
			continue
		}
		out.WriteString(strings.ToUpper(part[:1]))
		out.WriteString(part[1:])
	}
	return out.String()
}

// kindToType maps a protoreflect kind onto the descriptor enum.
func kindToType(kind protoreflect.Kind) descriptorpb.FieldDescriptorProto_Type {
	switch kind {
	case protoreflect.StringKind:
		return descriptorpb.FieldDescriptorProto_TYPE_STRING
	case protoreflect.BoolKind:
		return descriptorpb.FieldDescriptorProto_TYPE_BOOL
	case protoreflect.Int32Kind:
		return descriptorpb.FieldDescriptorProto_TYPE_INT32
	case protoreflect.Int64Kind:
		return descriptorpb.FieldDescriptorProto_TYPE_INT64
	case protoreflect.Uint32Kind:
		return descriptorpb.FieldDescriptorProto_TYPE_UINT32
	case protoreflect.Uint64Kind:
		return descriptorpb.FieldDescriptorProto_TYPE_UINT64
	case protoreflect.FloatKind:
		return descriptorpb.FieldDescriptorProto_TYPE_FLOAT
	case protoreflect.DoubleKind:
		return descriptorpb.FieldDescriptorProto_TYPE_DOUBLE
	case protoreflect.BytesKind:
		return descriptorpb.FieldDescriptorProto_TYPE_BYTES
	case protoreflect.MessageKind:
		return descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	default:
		return descriptorpb.FieldDescriptorProto_TYPE_STRING
	}
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

func strPtr(s string) *string { return &s }
func int32Ptr(i int32) *int32 { return &i }
func boolPtr(b bool) *bool    { return &b }

// fileDescriptor assembles everything derived so far into a protobuf file.
//
// Both halves need this: the server builds a file containing its services, and a
// client builds one containing only the messages it sends. protodesc validates the
// whole thing — duplicate field numbers, dangling type references, malformed names
// — so a schema mistake surfaces here rather than as a decode failure on the wire.
func (b *schemaBuilder) fileDescriptor(services ...*descriptorpb.ServiceDescriptorProto) (protoreflect.FileDescriptor, error) {
	for _, message := range b.messages {
		finalizeOptional(message)
	}

	file := &descriptorpb.FileDescriptorProto{
		Name:        strPtr(fileNameFor(b.pkg)),
		Package:     strPtr(b.pkg),
		Syntax:      strPtr("proto3"),
		MessageType: b.messages,
		Service:     services,
	}
	if b.needsTime {
		file.Dependency = append(file.Dependency, timestampProto)
	}

	descriptor, err := protodesc.NewFile(file, &compositeResolver{})
	if err != nil {
		return nil, fmt.Errorf("grpcapi: the derived schema is not a valid protobuf file: %w", err)
	}
	return descriptor, nil
}
