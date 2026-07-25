package nika

import (
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseGuardTag(t *testing.T) {
	tests := []struct {
		name    string
		tag     string
		want    []GuardSpec
		wantErr bool
	}{
		{name: "empty tag", tag: "", want: nil},
		{name: "whitespace only", tag: "   ", want: nil},
		{
			// The regexp this replaced required parentheses, so a bare guard name
			// silently registered nothing and left the route unprotected.
			name: "bare name",
			tag:  "Auth",
			want: []GuardSpec{{Name: "Auth"}},
		},
		{
			name: "empty parentheses",
			tag:  "Auth()",
			want: []GuardSpec{{Name: "Auth"}},
		},
		{
			name: "single argument",
			tag:  "Roles(admin)",
			want: []GuardSpec{{Name: "Roles", Args: []string{"admin"}}},
		},
		{
			name: "multiple arguments are trimmed",
			tag:  "Roles(admin, editor ,  viewer)",
			want: []GuardSpec{{Name: "Roles", Args: []string{"admin", "editor", "viewer"}}},
		},
		{
			name: "two guards space separated",
			tag:  "Auth Roles(admin)",
			want: []GuardSpec{{Name: "Auth"}, {Name: "Roles", Args: []string{"admin"}}},
		},
		{
			name: "two guards comma separated",
			tag:  "Auth,Roles(admin)",
			want: []GuardSpec{{Name: "Auth"}, {Name: "Roles", Args: []string{"admin"}}},
		},
		{
			name: "three bare guards",
			tag:  "Auth Verified Active",
			want: []GuardSpec{{Name: "Auth"}, {Name: "Verified"}, {Name: "Active"}},
		},
		{
			// A quoted argument must survive its own comma; splitting on it would
			// turn one scope into two bogus ones.
			name: "single-quoted argument keeps its comma",
			tag:  "Scope('user:read,user:write')",
			want: []GuardSpec{{Name: "Scope", Args: []string{"user:read,user:write"}}},
		},
		{
			name: "double-quoted argument keeps its spaces",
			tag:  `Message("hello world")`,
			want: []GuardSpec{{Name: "Message", Args: []string{"hello world"}}},
		},
		{
			name: "quoted empty argument is preserved",
			tag:  `Header("")`,
			want: []GuardSpec{{Name: "Header", Args: []string{""}}},
		},
		{
			name: "guard with parens followed by a bare guard",
			tag:  "Roles(admin) Verified",
			want: []GuardSpec{{Name: "Roles", Args: []string{"admin"}}, {Name: "Verified"}},
		},
		{name: "unterminated argument list", tag: "Roles(admin", wantErr: true},
		{name: "unmatched closing paren", tag: "Roles)", wantErr: true},
		{name: "open paren without a name", tag: "(admin)", wantErr: true},
		{name: "only punctuation", tag: ",,,", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseGuardTag(test.tag)

			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseGuardTag(%q) = %v, want an error", test.tag, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseGuardTag(%q) returned an unexpected error: %v", test.tag, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("ParseGuardTag(%q)\n  got:  %+v\n  want: %+v", test.tag, got, test.want)
			}
		})
	}
}

func TestAddGuardRejectsBadInput(t *testing.T) {
	t.Run("empty name panics", func(t *testing.T) {
		app := newTestApp()
		defer expectPanic(t, "guard name cannot be empty")
		app.AddGuard("", func([]string) gin.HandlerFunc { return nil })
	})

	t.Run("nil factory panics", func(t *testing.T) {
		app := newTestApp()
		defer expectPanic(t, "cannot be nil")
		app.AddGuard("Auth", nil)
	})

	t.Run("duplicate registration panics", func(t *testing.T) {
		app := newTestApp()
		noop := func([]string) gin.HandlerFunc {
			return func(c *gin.Context) { c.Next() }
		}
		app.AddGuard("Auth", noop)

		defer expectPanic(t, "already registered")
		app.AddGuard("Auth", noop)
	})
}

func TestGuardLookup(t *testing.T) {
	app := newTestApp()
	app.AddGuard("Auth", func([]string) gin.HandlerFunc {
		return func(c *gin.Context) { c.Next() }
	})

	if !app.HasGuard("Auth") {
		t.Error("HasGuard(\"Auth\") = false, want true")
	}
	if app.HasGuard("Missing") {
		t.Error("HasGuard(\"Missing\") = true, want false")
	}
	if _, ok := app.Guard("Auth"); !ok {
		t.Error("Guard(\"Auth\") reported not found")
	}
	if _, ok := app.Guard("Missing"); ok {
		t.Error("Guard(\"Missing\") reported found")
	}
}

// TestGuardArgumentsReachTheFactory pins the contract that guard tag arguments
// arrive at the factory in declaration order.
func TestGuardArgumentsReachTheFactory(t *testing.T) {
	app := newTestApp()

	var received []string
	app.AddGuard("Roles", func(args []string) gin.HandlerFunc {
		received = args
		return func(c *gin.Context) { c.Next() }
	})

	type controller struct {
		List func(*gin.Context) `route:"GET:/admin" guard:"Roles(admin, editor)"`
	}
	app.RegisterControllers(&controller{List: func(c *gin.Context) {}})

	want := []string{"admin", "editor"}
	if !reflect.DeepEqual(received, want) {
		t.Errorf("guard received %v, want %v", received, want)
	}
}
