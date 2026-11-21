package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/protocol"
)

type Service struct {
	userRepo  repository.UserRepository
	jwtSecret []byte
}

func NewService(userRepo repository.UserRepository, secret string) *Service {
	if secret == "" {
		secret = "procloud-default-secret-key-change-in-prod"
	}
	return &Service{
		userRepo:  userRepo,
		jwtSecret: []byte(secret),
	}
}

type Claims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Exp      int64  `json:"exp"`
}

// Register creates an administrator or standard user.
func (s *Service) Register(ctx context.Context, username, email, password, role string) (*protocol.User, error) {
	if role == "" {
		count, _ := s.userRepo.CountUsers(ctx)
		if count == 0 {
			role = "admin"
		} else {
			role = "member"
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	user := &protocol.User{
		ID:           fmt.Sprintf("usr-%d", time.Now().UnixNano()),
		Username:     username,
		Email:        email,
		PasswordHash: string(hash),
		Role:         role,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := s.userRepo.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	return user, nil
}

// Login authenticates credentials and produces a signed token.
func (s *Service) Login(ctx context.Context, username, password string) (string, *protocol.User, error) {
	user, err := s.userRepo.GetUserByUsername(ctx, username)
	if err != nil {
		return "", nil, errors.New("invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", nil, errors.New("invalid credentials")
	}

	token, err := s.generateToken(user)
	if err != nil {
		return "", nil, err
	}

	return token, user, nil
}

func (s *Service) generateToken(user *protocol.User) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims := Claims{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
		Exp:      time.Now().Add(24 * time.Hour).Unix(),
	}
	claimsBytes, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(claimsBytes)

	data := fmt.Sprintf("%s.%s", header, payload)
	mac := hmac.New(sha256.New, s.jwtSecret)
	mac.Write([]byte(data))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("%s.%s", data, signature), nil
}

// VerifyToken validates JWT signature and parses claims.
func (s *Service) VerifyToken(tokenString string) (*Claims, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid token format")
	}

	data := fmt.Sprintf("%s.%s", parts[0], parts[1])
	mac := hmac.New(sha256.New, s.jwtSecret)
	mac.Write([]byte(data))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return nil, errors.New("invalid token signature")
	}

	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}

	var claims Claims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return nil, err
	}

	if claims.Exp < time.Now().Unix() {
		return nil, errors.New("token expired")
	}

	return &claims, nil
}
