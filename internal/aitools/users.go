package aitools

import (
	"context"
	"sort"
	"strings"

	"windshift/internal/auth"
	"windshift/internal/repository"
	"windshift/internal/services"
)

// findUsersMaxResults caps find_users output so a broad query can't flood
// the model's context with the whole user directory.
const findUsersMaxResults = 20

type findUsersArgs struct {
	Query       string `json:"query" jsonschema:"Case-insensitive substring matched against user names and emails"`
	WorkspaceID *int   `json:"workspace_id,omitempty" jsonschema:"Optional workspace ID. If set, only that workspace's members are searched and their workspace roles are included."`
}

type foundUserDTO struct {
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	Username string   `json:"username,omitempty"`
	Email    string   `json:"email,omitempty"`
	Roles    []string `json:"workspace_roles,omitempty"`
}

type findUsersOut struct {
	Users []foundUserDTO `json:"users"`
}

func init() {
	Register(Default, Tool[findUsersArgs]{
		Name:        "find_users",
		Scopes:      []string{auth.ScopeUsersRead},
		Description: "Find users by name or email (case-insensitive substring match). Pass workspace_id to restrict the search to that workspace's members; member results include their workspace role names. Returns at most 20 matches — narrow the query if the person you want is missing.",
		Run: func(_ context.Context, env *Env, args findUsersArgs) (any, error) {
			query := strings.ToLower(strings.TrimSpace(args.Query))
			if query == "" {
				return map[string]string{"error": "query is required"}, nil
			}

			var candidates []foundUserDTO
			if args.WorkspaceID != nil {
				if !env.HasWorkspaceAccess(*args.WorkspaceID) {
					return map[string]string{"error": "workspace not found"}, nil
				}
				assignments, err := repository.NewWorkspaceRoleRepository(env.DB).ListUserAssignments(*args.WorkspaceID)
				if err != nil {
					return nil, err
				}
				// One row per user↔role pair; fold roles into a single entry per user.
				byID := map[int]*foundUserDTO{}
				var order []int
				for _, a := range assignments {
					u, exists := byID[a.UserID]
					if !exists {
						first, last := "", ""
						if a.FirstName != nil {
							first = *a.FirstName
						}
						if a.LastName != nil {
							last = *a.LastName
						}
						u = &foundUserDTO{
							ID:       a.UserID,
							Name:     displayName(first, last, a.Username),
							Username: a.Username,
							Email:    a.Email,
						}
						byID[a.UserID] = u
						order = append(order, a.UserID)
					}
					u.Roles = append(u.Roles, a.RoleName)
				}
				for _, id := range order {
					candidates = append(candidates, *byID[id])
				}
			} else {
				users, err := services.NewUserReadService(env.DB).ListAll()
				if err != nil {
					return nil, err
				}
				for _, u := range users {
					candidates = append(candidates, foundUserDTO{
						ID:       u.ID,
						Name:     displayName(u.FirstName, u.LastName, u.Username),
						Username: u.Username,
						Email:    u.Email,
					})
				}
			}

			out := findUsersOut{Users: []foundUserDTO{}}
			for _, c := range candidates {
				if strings.Contains(strings.ToLower(c.Name), query) ||
					strings.Contains(strings.ToLower(c.Username), query) ||
					strings.Contains(strings.ToLower(c.Email), query) {
					out.Users = append(out.Users, c)
				}
			}
			sort.Slice(out.Users, func(i, j int) bool {
				if out.Users[i].Name != out.Users[j].Name {
					return out.Users[i].Name < out.Users[j].Name
				}
				return out.Users[i].ID < out.Users[j].ID
			})
			if len(out.Users) > findUsersMaxResults {
				out.Users = out.Users[:findUsersMaxResults]
			}
			return out, nil
		},
	})
}

// displayName builds a user's full name from first/last, falling back to the
// username when both are empty.
func displayName(first, last, username string) string {
	name := strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
	if name == "" {
		return username
	}
	return name
}
