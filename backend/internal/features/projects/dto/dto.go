package projects_dto

import (
	"time"

	users_enums "obzly/internal/features/users/enums"

	"github.com/google/uuid"
)

type AddMemberStatus string

const (
	AddStatusInvited AddMemberStatus = "INVITED"
	AddStatusAdded   AddMemberStatus = "ADDED"
)

// Project DTOs
type CreateProjectRequestDTO struct {
	Name string `json:"name" binding:"required,min=1,max=255"`
}

type ProjectResponseDTO struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`

	// User's role in this project (populated when fetching for specific user)
	UserRole *users_enums.ProjectRole `json:"userRole,omitempty"`

	// IsSystem: the reserved project holding Obzly's own request traces.
	IsSystem bool `json:"isSystem"`
}

type ListProjectsResponseDTO struct {
	Projects []ProjectResponseDTO `json:"projects"`
}

// Membership DTOs
type AddMemberRequestDTO struct {
	Email string                  `json:"email" binding:"required,email"`
	Role  users_enums.ProjectRole `json:"role"  binding:"required"`
}

type AddMemberResponseDTO struct {
	Status AddMemberStatus `json:"status"`
	// InviteToken is the raw invite token (shown once), set only when Status is
	// INVITED; the admin relays it since there's no email system.
	InviteToken string `json:"inviteToken,omitempty"`
}

type ChangeMemberRoleRequestDTO struct {
	Role users_enums.ProjectRole `json:"role" binding:"required"`
}

type TransferOwnershipRequestDTO struct {
	NewOwnerEmail string `json:"newOwnerEmail" binding:"required,email"`
}

type ProjectMemberResponseDTO struct {
	ID        uuid.UUID               `json:"id"`
	UserID    uuid.UUID               `json:"userId"`
	Email     string                  `json:"email"` // Populated from user join
	Name      string                  `json:"name"`  // Populated from user join
	Role      users_enums.ProjectRole `json:"role"`
	CreatedAt time.Time               `json:"createdAt"`
}

type GetMembersResponseDTO struct {
	Members []ProjectMemberResponseDTO `json:"members"`
}
