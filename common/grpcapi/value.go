package grpcapi

import (
	"fmt"
	"reflect"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Conversion between a Go struct and a dynamic protobuf message.
//
// The mapping is driven by the bindings computed when the descriptor was built,
// so no reflection over names happens per request — a field is found by index.
//
// This is deliberately not a bridge through JSON. protojson encodes an int64 as a
// quoted string, and Go's encoding/json will not read that back into an int64
// field, so every id in the system would break. Converting directly also avoids
// serialising each message twice on a path where that is the dominant cost.

// toDynamic copies a Go struct into a protobuf message.
func toDynamic(binding *messageBinding, src reflect.Value, dst protoreflect.Message) error {
	src = reflect.Indirect(src)
	if !src.IsValid() {
		// A nil response is a programming error the handler should have reported
		// as an error; producing an empty message would hide it.
		return fmt.Errorf("grpcapi: %s: nil value", binding.name)
	}

	for _, field := range binding.fields {
		goValue := src.Field(field.goIndex)

		if field.optional {
			if goValue.IsNil() {
				// Absent, not zero. Leaving the field unset is what preserves the
				// distinction on the wire.
				continue
			}
			goValue = goValue.Elem()
		}

		descriptor := dst.Descriptor().Fields().ByNumber(field.number)
		if descriptor == nil {
			return fmt.Errorf("grpcapi: %s: no descriptor for field %d", binding.name, field.number)
		}

		if field.repeated {
			if goValue.IsNil() || goValue.Len() == 0 {
				continue
			}
			list := dst.Mutable(descriptor).List()
			for i := 0; i < goValue.Len(); i++ {
				value, err := scalarToProto(field, descriptor, goValue.Index(i), dst)
				if err != nil {
					return fmt.Errorf("grpcapi: %s.%s[%d]: %w", binding.name, field.name, i, err)
				}
				list.Append(value)
			}
			continue
		}

		// Skip zero values on non-optional scalars: proto3 does not put them on
		// the wire either, so writing them explicitly only costs bytes.
		if !field.optional && isZero(goValue) && field.nested == nil && !field.isTime {
			continue
		}

		value, err := scalarToProto(field, descriptor, goValue, dst)
		if err != nil {
			return fmt.Errorf("grpcapi: %s.%s: %w", binding.name, field.name, err)
		}
		dst.Set(descriptor, value)
	}

	return nil
}

// scalarToProto converts one Go value to a protobuf value.
func scalarToProto(
	field *fieldBinding,
	descriptor protoreflect.FieldDescriptor,
	goValue reflect.Value,
	parent protoreflect.Message,
) (protoreflect.Value, error) {
	goValue = reflect.Indirect(goValue)

	if field.isTime {
		instant, ok := goValue.Interface().(time.Time)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("expected time.Time, got %s", goValue.Type())
		}
		message := dynamicpb.NewMessage(descriptor.Message())
		fields := descriptor.Message().Fields()
		message.Set(fields.ByName("seconds"), protoreflect.ValueOfInt64(instant.Unix()))
		message.Set(fields.ByName("nanos"), protoreflect.ValueOfInt32(int32(instant.Nanosecond())))
		return protoreflect.ValueOfMessage(message), nil
	}

	if field.isBytes {
		return protoreflect.ValueOfBytes(goValue.Bytes()), nil
	}

	if field.nested != nil {
		message := dynamicpb.NewMessage(descriptor.Message())
		if err := toDynamic(field.nested, goValue, message); err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfMessage(message), nil
	}

	switch field.kind {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(goValue.String()), nil
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(goValue.Bool()), nil
	case protoreflect.Int32Kind:
		return protoreflect.ValueOfInt32(int32(goValue.Int())), nil
	case protoreflect.Int64Kind:
		return protoreflect.ValueOfInt64(goValue.Int()), nil
	case protoreflect.Uint32Kind:
		return protoreflect.ValueOfUint32(uint32(goValue.Uint())), nil
	case protoreflect.Uint64Kind:
		return protoreflect.ValueOfUint64(goValue.Uint()), nil
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(float32(goValue.Float())), nil
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(goValue.Float()), nil
	default:
		return protoreflect.Value{}, fmt.Errorf("unsupported kind %s", field.kind)
	}
}

// fromDynamic copies a protobuf message into a Go struct.
func fromDynamic(binding *messageBinding, src protoreflect.Message, dst reflect.Value) error {
	dst = reflect.Indirect(dst)
	if dst.Kind() != reflect.Struct {
		return fmt.Errorf("grpcapi: %s: destination is %s, want a struct", binding.name, dst.Kind())
	}

	for _, field := range binding.fields {
		descriptor := src.Descriptor().Fields().ByNumber(field.number)
		if descriptor == nil {
			continue
		}

		target := dst.Field(field.goIndex)
		if !target.CanSet() {
			continue
		}

		if field.repeated {
			if !src.Has(descriptor) {
				continue
			}
			list := src.Get(descriptor).List()
			slice := reflect.MakeSlice(reflect.SliceOf(field.goType), 0, list.Len())
			for i := 0; i < list.Len(); i++ {
				element := reflect.New(field.goType).Elem()
				if err := scalarFromProto(field, descriptor, list.Get(i), element); err != nil {
					return fmt.Errorf("grpcapi: %s.%s[%d]: %w", binding.name, field.name, i, err)
				}
				slice = reflect.Append(slice, element)
			}
			target.Set(slice)
			continue
		}

		// An unset optional field stays nil, which is what lets a handler tell
		// "leave this alone" from "set it to empty".
		if field.optional && !src.Has(descriptor) {
			continue
		}

		holder := target
		if field.optional {
			holder = reflect.New(field.goType).Elem()
		}

		if err := scalarFromProto(field, descriptor, src.Get(descriptor), holder); err != nil {
			return fmt.Errorf("grpcapi: %s.%s: %w", binding.name, field.name, err)
		}

		if field.optional {
			pointer := reflect.New(field.goType)
			pointer.Elem().Set(holder)
			target.Set(pointer)
		}
	}

	return nil
}

// scalarFromProto converts one protobuf value into a Go value.
func scalarFromProto(
	field *fieldBinding,
	descriptor protoreflect.FieldDescriptor,
	value protoreflect.Value,
	target reflect.Value,
) error {
	if field.isTime {
		message := value.Message()
		fields := message.Descriptor().Fields()
		seconds := message.Get(fields.ByName("seconds")).Int()
		nanos := message.Get(fields.ByName("nanos")).Int()
		target.Set(reflect.ValueOf(time.Unix(seconds, nanos).UTC()))
		return nil
	}

	if field.isBytes {
		// Copy: the buffer behind a decoded message is not ours to retain.
		raw := value.Bytes()
		out := make([]byte, len(raw))
		copy(out, raw)
		target.SetBytes(out)
		return nil
	}

	if field.nested != nil {
		nested := target
		if nested.Kind() == reflect.Ptr {
			nested.Set(reflect.New(nested.Type().Elem()))
			nested = nested.Elem()
		}
		return fromDynamic(field.nested, value.Message(), nested)
	}

	switch field.kind {
	case protoreflect.StringKind:
		target.SetString(value.String())
	case protoreflect.BoolKind:
		target.SetBool(value.Bool())
	case protoreflect.Int32Kind, protoreflect.Int64Kind:
		if target.OverflowInt(value.Int()) {
			return fmt.Errorf("%d overflows %s", value.Int(), target.Type())
		}
		target.SetInt(value.Int())
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind:
		if target.OverflowUint(value.Uint()) {
			return fmt.Errorf("%d overflows %s", value.Uint(), target.Type())
		}
		target.SetUint(value.Uint())
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		target.SetFloat(value.Float())
	default:
		return fmt.Errorf("unsupported kind %s", field.kind)
	}
	return nil
}

// isZero reports whether a value is its type's zero, for proto3 default-omission.
func isZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Slice, reflect.Map:
		return v.IsNil() || v.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}
