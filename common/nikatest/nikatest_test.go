package nikatest_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
	"github.com/nika-framework/nika/common/microservice"
	"github.com/nika-framework/nika/common/nikatest"
)

// --- the application under test -------------------------------------------
//
// A small but realistic app: a repository behind an interface, a controller with
// HTTP routes and message handlers on the same struct, a guard, and validation.

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	// The hash must never be serialised; a test asserts that.
	PasswordHash string `json:"-"`
}

type UserRepository interface {
	FindAll() []User
	FindByID(id string) (User, bool)
	Create(name, email string) User
}

type memoryUserRepo struct {
	users  []User
	nextID int
}

func newMemoryUserRepo(seed ...User) *memoryUserRepo {
	return &memoryUserRepo{users: seed, nextID: len(seed) + 1}
}

func (r *memoryUserRepo) FindAll() []User { return r.users }

func (r *memoryUserRepo) FindByID(id string) (User, bool) {
	for _, user := range r.users {
		if user.ID == id {
			return user, true
		}
	}
	return User{}, false
}

func (r *memoryUserRepo) Create(name, email string) User {
	user := User{ID: itoa(r.nextID), Name: name, Email: email, PasswordHash: "secret-hash"}
	r.nextID++
	r.users = append(r.users, user)
	return user
}

type CreateUserDto struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type UserController struct {
	// The same handler serves an HTTP route and a message subject.
	List   func(*gin.Context) `route:"GET:/users"    transport:"memory" pattern:"users"`
	Get    func(*gin.Context) `route:"GET:/users/:id" transport:"memory" pattern:"user_*"`
	Create func(*gin.Context) `route:"POST:/users"   transport:"memory" pattern:"user_created"`
	Admin  func(*gin.Context) `route:"DELETE:/users/:id" guard:"Admin"`
}

func newUserController(repo UserRepository) *UserController {
	return &UserController{
		List: func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"success": true, "data": repo.FindAll()})
		},

		Get: func(c *gin.Context) {
			// The id arrives as a path parameter over HTTP and as the literal
			// subject over a message transport.
			id := c.Param("id")
			if id == "" {
				id = trimPrefix(microservice.PatternFrom(c), "user_")
			}

			user, found := repo.FindByID(id)
			if !found {
				c.JSON(http.StatusNotFound, gin.H{
					"success": false,
					"error":   gin.H{"code": 404, "message": "USER_NOT_FOUND"},
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{"success": true, "data": user})
		},

		Create: func(c *gin.Context) {
			var dto CreateUserDto
			if err := c.ShouldBindJSON(&dto); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"success": false,
					"error":   gin.H{"code": 400, "message": "INVALID_JSON"},
				})
				return
			}

			var details []gin.H
			if dto.Name == "" {
				details = append(details, gin.H{"field": "name", "message": "This field is required"})
			}
			if dto.Email == "" {
				details = append(details, gin.H{"field": "email", "message": "This field is required"})
			}
			if len(details) > 0 {
				c.JSON(http.StatusUnprocessableEntity, gin.H{
					"success": false,
					"error":   gin.H{"code": 422, "message": "VALIDATION_ERROR", "details": details},
				})
				return
			}

			c.JSON(http.StatusCreated, gin.H{"success": true, "data": repo.Create(dto.Name, dto.Email)})
		},

		Admin: func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"success": true, "message": "deleted"})
		},
	}
}

type userModule struct {
	repo UserRepository
}

func (m userModule) Imports() []nika.Module { return nil }
func (m userModule) Providers() []any {
	if m.repo != nil {
		return []any{m.repo}
	}
	return nil
}
func (m userModule) Controllers() []any { return []any{newUserController} }
func (m userModule) Exports() []any     { return nil }

// --- HTTP tests -----------------------------------------------------------

func TestAPIGet(t *testing.T) {
	app := boot(t, newMemoryUserRepo(
		User{ID: "1", Name: "Ada", Email: "ada@example.com"},
		User{ID: "2", Name: "Grace", Email: "grace@example.com"},
	))

	app.GET("/users").Do().
		ExpectOK().
		ExpectJSONContentType().
		ExpectJSONPath("success", true).
		ExpectJSONLen("data", 2).
		ExpectJSONPath("data.0.name", "Ada").
		ExpectJSONPath("data.1.email", "grace@example.com")
}

func TestAPIGetSubsetMatching(t *testing.T) {
	app := boot(t, newMemoryUserRepo(User{ID: "1", Name: "Ada", Email: "ada@example.com"}))

	// A subset assertion survives an unrelated field being added to the response,
	// which is what keeps a suite from needing a blanket update on every change.
	app.GET("/users/1").Do().
		ExpectOK().
		ExpectJSON(`{"success": true, "data": {"name": "Ada"}}`)
}

func TestAPIPostCreates(t *testing.T) {
	repo := newMemoryUserRepo()
	app := boot(t, repo)

	app.POST("/users").
		JSON(CreateUserDto{Name: "Ada", Email: "ada@example.com"}).
		Do().
		ExpectCreated().
		ExpectAPISuccess().
		ExpectJSONPath("data.name", "Ada").
		ExpectJSONPathExists("data.id")

	if len(repo.FindAll()) != 1 {
		t.Errorf("the repository holds %d users, want 1", len(repo.FindAll()))
	}
}

// TestSensitiveFieldsAreNotSerialised is the assertion worth having in every
// API suite: a `json:"-"` that gets dropped in a refactor leaks credentials.
func TestSensitiveFieldsAreNotSerialised(t *testing.T) {
	app := boot(t, newMemoryUserRepo())

	app.POST("/users").
		JSON(CreateUserDto{Name: "Ada", Email: "ada@example.com"}).
		Do().
		ExpectCreated().
		ExpectJSONPathAbsent("data.PasswordHash", "data.password_hash").
		ExpectNotContains("secret-hash")
}

func TestAPIValidationErrors(t *testing.T) {
	app := boot(t, newMemoryUserRepo())

	app.POST("/users").
		JSON(CreateUserDto{}).
		Do().
		ExpectUnprocessable().
		ExpectAPIError("VALIDATION_ERROR").
		ExpectValidationError("name", "email")
}

func TestAPIMalformedJSON(t *testing.T) {
	app := boot(t, newMemoryUserRepo())

	// A raw string body is sent verbatim, which is how a test reaches the parse
	// error path rather than the validation one.
	app.POST("/users").
		JSON(`{"name": `).
		Do().
		ExpectBadRequest().
		ExpectAPIError("INVALID_JSON")
}

func TestAPINotFound(t *testing.T) {
	app := boot(t, newMemoryUserRepo())

	app.GET("/users/999").Do().
		ExpectNotFound().
		ExpectAPIError("USER_NOT_FOUND")
}

func TestQueryAndHeaders(t *testing.T) {
	var (
		seenQuery  string
		seenHeader string
	)

	app := nikatest.New(t)
	app.Header("X-API-Version", "2")
	app.RegisterControllers(&struct {
		Search func(*gin.Context) `route:"GET:/search"`
	}{
		Search: func(c *gin.Context) {
			seenQuery = c.Query("q")
			seenHeader = c.GetHeader("X-API-Version")
			c.JSON(http.StatusOK, gin.H{"success": true})
		},
	})

	app.GET("/search").Query("q", "ada lovelace").Do().ExpectOK()

	if seenQuery != "ada lovelace" {
		t.Errorf("q = %q, want \"ada lovelace\"", seenQuery)
	}
	// A default header set on the app must apply to every request.
	if seenHeader != "2" {
		t.Errorf("X-API-Version = %q, want \"2\"", seenHeader)
	}
}

func TestGuardStubbingAndDenial(t *testing.T) {
	t.Run("stubbed guard lets the request through", func(t *testing.T) {
		app := nikatest.New(t).StubGuard("Admin")
		app.LoadModule(userModule{repo: newMemoryUserRepo(User{ID: "1"})})

		app.DELETE("/users/1").Do().ExpectOK().ExpectJSONPath("message", "deleted")
	})

	t.Run("denying guard blocks the handler", func(t *testing.T) {
		app := nikatest.New(t).DenyGuard("Admin", http.StatusForbidden)
		app.LoadModule(userModule{repo: newMemoryUserRepo(User{ID: "1"})})

		app.DELETE("/users/1").Do().
			ExpectForbidden().
			ExpectNotContains("deleted")
	})
}

func TestRouteSurfaceAssertions(t *testing.T) {
	app := boot(t, newMemoryUserRepo())

	app.ExpectRoute("GET", "/users").
		ExpectRoute("POST", "/users").
		ExpectRoute("GET", "/users/:id").
		// A debug endpoint that leaked into a build is caught here rather than in
		// production.
		ExpectNoRoute("GET", "/debug/pprof")
}

func TestDIOverride(t *testing.T) {
	// An override replaces what the module would otherwise provide, which is how
	// a controller is tested without its real database.
	fake := newMemoryUserRepo(User{ID: "9", Name: "Fake"})

	app := nikatest.New(t).StubGuard("Admin")
	nikatest.OverrideAs[UserRepository](app, fake)
	app.LoadModule(userModule{})

	app.GET("/users").Do().
		ExpectOK().
		ExpectJSONPath("data.0.name", "Fake")
}

func TestDecodeIntoAStruct(t *testing.T) {
	app := boot(t, newMemoryUserRepo(User{ID: "1", Name: "Ada", Email: "ada@example.com"}))

	var body struct {
		Success bool   `json:"success"`
		Data    []User `json:"data"`
	}
	app.GET("/users").Do().ExpectOK().DecodeJSON(&body)

	if !body.Success || len(body.Data) != 1 || body.Data[0].Name != "Ada" {
		t.Errorf("decoded body = %+v, want one user named Ada", body)
	}
}

func TestFormAndMultipartBodies(t *testing.T) {
	var (
		seenField    string
		seenFilename string
		seenSize     int64
	)

	app := nikatest.New(t)
	app.RegisterControllers(&struct {
		Upload func(*gin.Context) `route:"POST:/upload"`
		Submit func(*gin.Context) `route:"POST:/submit"`
	}{
		Upload: func(c *gin.Context) {
			header, err := c.FormFile("avatar")
			if err != nil {
				c.String(http.StatusBadRequest, err.Error())
				return
			}
			seenFilename = header.Filename
			seenSize = header.Size
			seenField = c.PostForm("caption")
			c.JSON(http.StatusCreated, gin.H{"success": true})
		},
		Submit: func(c *gin.Context) {
			seenField = c.PostForm("name")
			c.JSON(http.StatusOK, gin.H{"success": true})
		},
	})

	app.POST("/upload").Multipart(func(m *nikatest.Multipart) {
		m.Field("caption", "my avatar")
		m.File("avatar", "a.png", []byte("\x89PNG fake bytes"))
	}).Do().ExpectCreated()

	if seenFilename != "a.png" {
		t.Errorf("filename = %q, want \"a.png\"", seenFilename)
	}
	if seenSize != int64(len("\x89PNG fake bytes")) {
		t.Errorf("file size = %d, want %d", seenSize, len("\x89PNG fake bytes"))
	}
	if seenField != "my avatar" {
		t.Errorf("caption = %q, want \"my avatar\"", seenField)
	}

	app.POST("/submit").Form(map[string]string{"name": "Ada"}).Do().ExpectOK()
	if seenField != "Ada" {
		t.Errorf("name = %q, want \"Ada\"", seenField)
	}
}

func TestContentAssertionsOnHTML(t *testing.T) {
	app := nikatest.New(t)
	app.RegisterControllers(&struct {
		Page func(*gin.Context) `route:"GET:/page"`
	}{
		Page: func(c *gin.Context) {
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusOK, "<html><body><h1>Welcome, Ada</h1></body></html>")
		},
	})

	app.GET("/page").Do().
		ExpectOK().
		ExpectHeaderContains("Content-Type", "text/html").
		ExpectContains("<h1>", "Welcome, Ada").
		ExpectNotContains("<script>", "TODO").
		ExpectMatches(`<h1>Welcome, \w+</h1>`)
}

// --- message tests --------------------------------------------------------

// TestMessageDispatch is the microservice half of the harness, exercising the
// same controller through the transport tags.
func TestMessageDispatch(t *testing.T) {
	repo := newMemoryUserRepo(User{ID: "7", Name: "Ada", Email: "ada@example.com"})

	ms := nikatest.NewMicroservice(t)
	ms.App().StubGuard("Admin")
	nikatest.OverrideAs[UserRepository](ms.App(), repo)
	ms.LoadModule(userModule{})

	t.Run("exact pattern", func(t *testing.T) {
		ms.Send("user_created", CreateUserDto{Name: "Grace", Email: "g@example.com"}).
			ExpectStatus(http.StatusCreated).
			ExpectNoError().
			ExpectJSONPath("data.name", "Grace")
	})

	t.Run("wildcard pattern receives the literal subject", func(t *testing.T) {
		ms.Send("user_7", nil).
			ExpectOK().
			ExpectNoError().
			ExpectJSONPath("data.name", "Ada")
	})

	t.Run("literal pattern", func(t *testing.T) {
		ms.Send("users", nil).
			ExpectOK().
			ExpectJSONPathExists("data")
	})

	t.Run("unrouted subject", func(t *testing.T) {
		ms.Send("order_created", nil).ExpectNoHandler()
	})
}

// TestMessageRoutingPrecedence documents the precedence rule as an executable
// assertion, since it is the behaviour most likely to regress.
func TestMessageRoutingPrecedence(t *testing.T) {
	ms := nikatest.NewMicroservice(t)
	ms.App().StubGuard("Admin")
	nikatest.OverrideAs[UserRepository](ms.App(), newMemoryUserRepo())
	ms.LoadModule(userModule{})

	ms.ExpectPattern("user_created").
		ExpectPattern("user_*").
		ExpectPattern("users").
		ExpectRoutesTo("user_created", "user_created").
		ExpectRoutesTo("user_23", "user_*").
		ExpectRoutesTo("users", "users")
}

func TestMessageValidationErrors(t *testing.T) {
	ms := nikatest.NewMicroservice(t)
	ms.App().StubGuard("Admin")
	nikatest.OverrideAs[UserRepository](ms.App(), newMemoryUserRepo())
	ms.LoadModule(userModule{})

	ms.Send("user_created", CreateUserDto{}).
		ExpectUnprocessable().
		ExpectError("VALIDATION_ERROR").
		ExpectValidationError("name", "email")

	ms.SendRaw("user_created", `{"name":`).
		ExpectBadRequest().
		ExpectError("INVALID_JSON")
}

func TestMessageClientRoundTrip(t *testing.T) {
	ms := nikatest.NewMicroservice(t)
	ms.App().StubGuard("Admin")
	nikatest.OverrideAs[UserRepository](ms.App(), newMemoryUserRepo())
	ms.LoadModule(userModule{})

	// A real client over the harness transport, for testing code that itself
	// calls out through *microservice.Client.
	client := ms.Client()

	var reply struct {
		Success bool `json:"success"`
		Data    User `json:"data"`
	}
	if err := client.Send(t.Context(), "user_created", CreateUserDto{Name: "Ada", Email: "a@b.c"}, &reply); err != nil {
		t.Fatalf("client.Send returned %v", err)
	}
	if !reply.Success || reply.Data.Name != "Ada" {
		t.Errorf("reply = %+v, want success with name Ada", reply)
	}
}

// TestSameControllerServesBothTransports is the payoff of putting `route` and
// `transport` tags on one field: the two entry points cannot drift apart.
func TestSameControllerServesBothTransports(t *testing.T) {
	repo := newMemoryUserRepo(User{ID: "1", Name: "Ada", Email: "ada@example.com"})

	app := nikatest.New(t).StubGuard("Admin")
	nikatest.OverrideAs[UserRepository](app, repo)
	ms := nikatest.Attach(app)
	ms.LoadModule(userModule{})

	httpBody := app.GET("/users").Do().ExpectOK().BodyString()
	msgBody := ms.Send("users", nil).ExpectOK().BodyString()

	if httpBody != msgBody {
		t.Errorf("the HTTP and message responses differ:\n  http:    %s\n  message: %s", httpBody, msgBody)
	}
}

// --- helpers --------------------------------------------------------------

func boot(t *testing.T, repo UserRepository) *nikatest.App {
	t.Helper()

	app := nikatest.New(t).StubGuard("Admin")
	nikatest.OverrideAs[UserRepository](app, repo)
	app.LoadModule(userModule{})
	return app
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}

func trimPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}
