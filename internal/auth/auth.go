package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/protocol"
)

var authSeq uint64

func genAuthID(prefix string) string {
	seq := atomic.AddUint64(&authSeq, 1)
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), seq)
}

type Service struct {
	userRepo  repository.UserRepository
	jwtSecret []byte
}

func NewService(userRepo repository.UserRepository, secret string) *Service {
	if secret == "" {
		if envSec := os.Getenv("PROCLOUD_JWT_SECRET"); envSec != "" {
			secret = envSec
		} else {
			secret = "procloud-default-secret-key-change-in-prod"
		}
	}
	return &Service{
		userRepo:  userRepo,
		jwtSecret: []byte(secret),
	}
}

type Claims struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	TenantID    string `json:"tenant_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Exp         int64  `json:"exp"`
}

// NormalizeRole maps legacy or alternate role names to standard RBAC roles.
func NormalizeRole(role string) string {
	r := strings.ToLower(strings.TrimSpace(role))
	switch r {
	case "superadmin", "support_admin", "admin", "administrator":
		return protocol.RoleSuperAdmin // "admin"
	case "org_admin", "tenant_admin", "manager":
		return protocol.RoleOrgAdmin
	case "developer", "operator", "member", "dev":
		return protocol.RoleDeveloper
	case "viewer", "auditor", "readonly":
		return protocol.RoleViewer
	default:
		return protocol.RoleDeveloper
	}
}

// Register creates an administrator or standard user.
func (s *Service) Register(ctx context.Context, username, email, password, role string) (*protocol.User, error) {
	return s.CreateUserWithOrg(ctx, username, email, password, role, "", username)
}

// CreateUserWithOrg registers a new user with organization affiliation and display name.
func (s *Service) CreateUserWithOrg(ctx context.Context, username, email, password, role, tenantID, displayName string) (*protocol.User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("username cannot be empty")
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, errors.New("email cannot be empty")
	}

	if len(password) < 6 {
		return nil, errors.New("password must be at least 6 characters")
	}

	if role == "" {
		count, _ := s.userRepo.CountUsers(ctx)
		if count == 0 {
			role = protocol.RoleSuperAdmin
		} else {
			role = protocol.RoleDeveloper
		}
	} else {
		role = NormalizeRole(role)
	}

	if displayName == "" {
		displayName = username
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	user := &protocol.User{
		ID:           genAuthID("usr"),
		Username:     username,
		Email:        email,
		PasswordHash: string(hash),
		Role:         role,
		TenantID:     tenantID,
		DisplayName:  displayName,
		Status:       "active",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := s.userRepo.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	return user, nil
}

// ListUsers retrieves users filtered optionally by tenant and role.
func (s *Service) ListUsers(ctx context.Context, tenantID, role string) ([]*protocol.User, error) {
	if role != "" {
		role = NormalizeRole(role)
	}
	return s.userRepo.ListUsers(ctx, tenantID, role)
}

// GetUser retrieves a single user by ID.
func (s *Service) GetUser(ctx context.Context, id string) (*protocol.User, error) {
	return s.userRepo.GetUserByID(ctx, id)
}

// UpdateUser modifies a user's role, tenant association, display name, and status.
func (s *Service) UpdateUser(ctx context.Context, id, role, tenantID, status, displayName string) (*protocol.User, error) {
	user, err := s.userRepo.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if role != "" {
		user.Role = NormalizeRole(role)
	}
	if tenantID != "" {
		user.TenantID = tenantID
	}
	if status != "" {
		user.Status = status
	}
	if displayName != "" {
		user.DisplayName = displayName
	}
	user.UpdatedAt = time.Now()

	if err := s.userRepo.UpdateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("failed to update user: %w", err)
	}
	return user, nil
}

// ResetPassword sets a new password for a user.
func (s *Service) ResetPassword(ctx context.Context, id, newPassword string) error {
	if len(newPassword) < 6 {
		return errors.New("password must be at least 6 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}
	return s.userRepo.UpdateUserPassword(ctx, id, string(hash))
}

// DeleteUser deletes a user by ID.
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	return s.userRepo.DeleteUser(ctx, id)
}

// RBACRoleDefinition provides metadata for roles in the UI.
type RBACRoleDefinition struct {
	Role        string   `json:"role"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scope       string   `json:"scope"`
	Permissions []string `json:"permissions"`
}

// GetRBACRoles returns standard roles and permissions matrix.
func GetRBACRoles() []RBACRoleDefinition {
	return []RBACRoleDefinition{
		{
			Role:        protocol.RoleSuperAdmin,
			Name:        "Super Admin / Support Admin",
			Description: "Global cluster administrator. Full control over all organizations, users, compute nodes, workloads, and audit logs.",
			Scope:       "Global Platform",
			Permissions: []string{
				"orgs:create", "orgs:read", "orgs:update", "orgs:delete",
				"users:create", "users:read", "users:update", "users:delete",
				"nodes:manage", "containers:manage", "pods:manage",
				"deployments:manage", "workflows:manage", "audit:view",
			},
		},
		{
			Role:        protocol.RoleOrgAdmin,
			Name:        "Organization Admin",
			Description: "Organization-level administrator. Manages team members, resources, workloads, and quotas within their organization.",
			Scope:       "Organization",
			Permissions: []string{
				"orgs:read", "orgs:update_profile",
				"users:create_in_org", "users:read_in_org", "users:update_in_org",
				"containers:manage", "pods:manage", "deployments:manage", "workflows:manage",
			},
		},
		{
			Role:        protocol.RoleDeveloper,
			Name:        "Developer / Operator",
			Description: "Can build and deploy workloads, run/stop containers and pods, view logs, and open terminal sessions within their organization.",
			Scope:       "Organization",
			Permissions: []string{
				"containers:create", "containers:read", "containers:start_stop", "containers:exec",
				"pods:create", "pods:read", "pods:start_stop", "logs:view", "workflows:run",
			},
		},
		{
			Role:        protocol.RoleViewer,
			Name:        "Auditor / Viewer",
			Description: "Read-only access. Can inspect active containers, pods, resource metrics, logs, and topologies without modification.",
			Scope:       "Organization",
			Permissions: []string{
				"containers:read", "pods:read", "deployments:read", "metrics:read", "logs:read",
			},
		},
	}
}

// Login authenticates credentials and produces a signed token.
func (s *Service) Login(ctx context.Context, username, password string) (string, *protocol.User, error) {
	user, err := s.userRepo.GetUserByUsername(ctx, username)
	if err != nil {
		return "", nil, errors.New("invalid credentials")
	}

	if user.Status == "suspended" {
		return "", nil, errors.New("account is suspended. Contact Support Admin.")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", nil, errors.New("invalid credentials")
	}

	now := time.Now()
	user.LastLoginAt = &now
	_ = s.userRepo.UpdateUser(ctx, user)

	token, err := s.generateToken(user)
	if err != nil {
		return "", nil, err
	}

	return token, user, nil
}

func (s *Service) generateToken(user *protocol.User) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims := Claims{
		UserID:      user.ID,
		Username:    user.Username,
		Role:        NormalizeRole(user.Role),
		TenantID:    user.TenantID,
		DisplayName: user.DisplayName,
		Exp:         time.Now().Add(24 * time.Hour).Unix(),
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
