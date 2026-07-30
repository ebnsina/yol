package org

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"slices"

	"github.com/ebnsina/yol/internal/httpx"
)

// Role is what someone may do inside an organization.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

// Capability is a single thing a role may or may not do.
type Capability string

const (
	CanManageOrganization Capability = "manageOrganization"
	CanManageMembers      Capability = "manageMembers"
	CanManageServers      Capability = "manageServers"
	CanDeploy             Capability = "deploy"
	CanViewLogs           Capability = "viewLogs"
)

// capabilities is the whole permission model, in one place.
var capabilities = map[Role][]Capability{
	RoleOwner:  {CanManageOrganization, CanManageMembers, CanManageServers, CanDeploy, CanViewLogs},
	RoleAdmin:  {CanManageMembers, CanManageServers, CanDeploy, CanViewLogs},
	RoleMember: {CanDeploy, CanViewLogs},
	RoleViewer: {CanViewLogs},
}

var allRoles = []Role{RoleOwner, RoleAdmin, RoleMember, RoleViewer}

// Permissions is the capability set sent to clients, so a client shows or hides controls
// from this rather than reimplementing the rules.
type Permissions struct {
	ManageOrganization bool `json:"manageOrganization"`
	ManageMembers      bool `json:"manageMembers"`
	ManageServers      bool `json:"manageServers"`
	Deploy             bool `json:"deploy"`
	ViewLogs           bool `json:"viewLogs"`
}

// PermissionsFor expands a role into the flags clients render.
func PermissionsFor(r Role) Permissions {
	return Permissions{
		ManageOrganization: r.Can(CanManageOrganization),
		ManageMembers:      r.Can(CanManageMembers),
		ManageServers:      r.Can(CanManageServers),
		Deploy:             r.Can(CanDeploy),
		ViewLogs:           r.Can(CanViewLogs),
	}
}

// Can reports whether the role has the capability.
func (r Role) Can(c Capability) bool {
	return slices.Contains(capabilities[r], c)
}

// Require returns an authorization error when the role lacks the capability.
func (r Role) Require(c Capability) error {
	if r.Can(c) {
		return nil
	}
	return httpx.NotAuthorized()
}

// Validate rejects an unknown role.
func (r Role) Validate() *httpx.Error {
	if slices.Contains(allRoles, r) {
		return nil
	}
	return httpx.InvalidInput("Please choose one of the available roles.").
		WithField("role", "This is not a role you can assign.")
}

const inviteTokenBytes = 32

// newInviteToken returns the token for the link and the hash to store, so a database leak
// does not yield usable invitation links.
func newInviteToken() (token string, hash []byte, err error) {
	raw := make([]byte, inviteTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate invitation token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashInviteToken(token), nil
}

func hashInviteToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
