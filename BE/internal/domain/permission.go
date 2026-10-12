package domain

const (
	PermWorkspaceManage   = "workspace:manage"
	PermWorkspaceTransfer = "workspace:transfer"
	PermTeamManage        = "team:manage"
	PermIssueCreate       = "issue:create"
	PermIssueRead         = "issue:read"
	PermIssueUpdate       = "issue:update"
	PermIssueDelete       = "issue:delete"
	PermIssueDeleteOwn    = "issue:delete_own"
	PermProjectManage     = "project:manage"
	PermLabelManage       = "label:manage"
	PermMemberInvite      = "member:invite"
	PermCycleManage       = "cycle:manage"
	PermViewManage        = "view:manage"
	PermDevMachineRead    = "dev_machine:read"
	PermDevMachineCreate  = "dev_machine:create"
	PermDevMachineManage  = "dev_machine:manage"
	PermDevMachineAdmin   = "dev_machine:admin"
	PermShareCreate       = "share:create"

	// Docs: guests may read (watch sessions, open shared pages), members
	// and above may create, edit, move and trash documents.
	PermDocRead   = "doc:read"
	PermDocCreate = "doc:create"
	PermDocUpdate = "doc:update"
	PermDocDelete = "doc:delete"
)

var RolePermissions = map[string][]string{
	RoleOwner: {
		PermWorkspaceManage, PermWorkspaceTransfer, PermTeamManage, PermIssueCreate, PermIssueRead,
		PermIssueUpdate, PermIssueDelete, PermIssueDeleteOwn, PermProjectManage, PermLabelManage,
		PermMemberInvite, PermCycleManage, PermViewManage,
		PermDevMachineRead, PermDevMachineCreate, PermDevMachineManage, PermDevMachineAdmin,
		PermShareCreate,
		PermDocRead, PermDocCreate, PermDocUpdate, PermDocDelete,
	},
	RoleAdmin: {
		PermWorkspaceTransfer, PermTeamManage, PermIssueCreate, PermIssueRead, PermIssueUpdate,
		PermIssueDelete, PermIssueDeleteOwn, PermProjectManage, PermLabelManage, PermMemberInvite,
		PermCycleManage, PermViewManage,
		PermDevMachineRead, PermDevMachineCreate, PermDevMachineManage, PermDevMachineAdmin,
		PermShareCreate,
		PermDocRead, PermDocCreate, PermDocUpdate, PermDocDelete,
	},
	RoleMember: {
		PermIssueCreate, PermIssueRead, PermIssueUpdate, PermIssueDeleteOwn, PermProjectManage,
		PermLabelManage, PermCycleManage, PermViewManage,
		PermDevMachineRead, PermDevMachineCreate, PermDevMachineManage,
		PermShareCreate,
		PermDocRead, PermDocCreate, PermDocUpdate, PermDocDelete,
	},
	RoleGuest: {
		PermIssueRead,
		PermDocRead,
	},
}

func HasPermission(role string, permission string) bool {
	perms, ok := RolePermissions[role]
	if !ok {
		return false
	}
	for _, p := range perms {
		if p == permission {
			return true
		}
	}
	return false
}
