package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

const tokenLifetime = 15 * time.Minute

type User struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}
type response struct {
	AccessToken string `json:"accessToken"`
	User        User   `json:"user"`
}
type registerRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}
type claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}
type Handler struct {
	pool   *pgxpool.Pool
	secret []byte
}

func New(pool *pgxpool.Pool, secret string) *Handler {
	return &Handler{pool: pool, secret: []byte(secret)}
}

func (h *Handler) RegisterRoutes(app *fiber.App) {
	app.Post("/auth/register", h.register)
	app.Post("/auth/login", h.login)
	app.Get("/me", h.me)
}

func (h *Handler) register(c *fiber.Ctx) error {
	var in registerRequest
	if err := c.BodyParser(&in); err != nil {
		return problem(c, fiber.StatusBadRequest, "invalid-request", "Invalid request", "Request body must be valid JSON.")
	}
	in.Name, in.Email, in.Role = strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.Email)), strings.TrimSpace(in.Role)
	if in.Name == "" || len(in.Name) > 100 || !strings.Contains(in.Email, "@") || len(in.Password) < 8 || (in.Role != "buyer" && in.Role != "jastiper") {
		return problem(c, fiber.StatusBadRequest, "invalid-request", "Invalid request", "Provide name, email, a password of at least 8 characters, and role buyer or jastiper.")
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		return problem(c, 500, "internal-error", "Internal server error", "Could not create account.")
	}
	u := User{ID: uuid.New(), Name: in.Name, Email: in.Email, Role: in.Role}
	err = h.pool.QueryRow(c.Context(), `INSERT INTO users (id,name,email,password_hash,role) VALUES ($1,$2,$3,$4,$5) RETURNING created_at`, u.ID, u.Name, u.Email, hash, u.Role).Scan(&u.CreatedAt)
	if err != nil {
		if isUnique(err) {
			return problem(c, fiber.StatusConflict, "email-taken", "Email already registered", "Use another email or log in.")
		}
		return problem(c, 500, "internal-error", "Internal server error", "Could not create account.")
	}
	return c.Status(fiber.StatusCreated).JSON(response{AccessToken: h.sign(u), User: u})
}

func (h *Handler) login(c *fiber.Ctx) error {
	var in loginRequest
	if err := c.BodyParser(&in); err != nil {
		return problem(c, fiber.StatusBadRequest, "invalid-request", "Invalid request", "Request body must be valid JSON.")
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	var u User
	var hash string
	err := h.pool.QueryRow(c.Context(), `SELECT id,name,email,password_hash,role,created_at FROM users WHERE LOWER(email)=LOWER($1)`, email).Scan(&u.ID, &u.Name, &u.Email, &hash, &u.Role, &u.CreatedAt)
	if err != nil || !verifyPassword(hash, in.Password) {
		return problem(c, fiber.StatusUnauthorized, "invalid-credentials", "Invalid credentials", "Email or password is incorrect.")
	}
	return c.JSON(response{AccessToken: h.sign(u), User: u})
}

func (h *Handler) me(c *fiber.Ctx) error {
	auth := strings.TrimPrefix(c.Get(fiber.HeaderAuthorization), "Bearer ")
	token, err := jwt.ParseWithClaims(auth, &claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return h.secret, nil
	})
	if err != nil || !token.Valid {
		return problem(c, fiber.StatusUnauthorized, "unauthorized", "Unauthorized", "Provide a valid access token.")
	}
	cl, ok := token.Claims.(*claims)
	if !ok {
		return problem(c, fiber.StatusUnauthorized, "unauthorized", "Unauthorized", "Provide a valid access token.")
	}
	id, err := uuid.Parse(cl.Subject)
	if err != nil {
		return problem(c, fiber.StatusUnauthorized, "unauthorized", "Unauthorized", "Provide a valid access token.")
	}
	var u User
	err = h.pool.QueryRow(c.Context(), `SELECT id,name,email,role,created_at FROM users WHERE id=$1`, id).Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.CreatedAt)
	if err != nil {
		return problem(c, fiber.StatusUnauthorized, "unauthorized", "Unauthorized", "Provide a valid access token.")
	}
	return c.JSON(u)
}

func (h *Handler) sign(u User) string {
	now := time.Now()
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{Role: u.Role, RegisteredClaims: jwt.RegisteredClaims{Subject: u.ID.String(), IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(tokenLifetime))}})
	s, _ := t.SignedString(h.secret)
	return s
}
func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=4$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}
func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return false
	}
	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[4])
	expected, e2 := base64.RawStdEncoding.DecodeString(parts[5])
	if e1 != nil || e2 != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
func problem(c *fiber.Ctx, status int, kind, title, detail string) error {
	return c.Status(status).Type("application/problem+json").JSON(fiber.Map{"type": "https://titipdong.local/problems/" + kind, "title": title, "status": status, "detail": detail, "instance": c.Path()})
}
