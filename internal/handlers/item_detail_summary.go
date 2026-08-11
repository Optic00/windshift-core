package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

// ItemDetailHandler composes the above-the-fold item context shared by the
// desktop and mobile detail surfaces. Heavy panels remain on their dedicated,
// deferred endpoints.
type ItemDetailHandler struct {
	item         *ItemHandler
	links        *ItemLinkHandler
	linkTypes    *LinkTypeHandler
	screens      *ScreenHandler
	requestTypes *RequestTypeHandler
	actions      *ActionsHandler
}

type ItemDetailScreenContext struct {
	Edit *models.Screen `json:"edit"`
	View *models.Screen `json:"view"`
}

type ItemDetailSummaryResponse struct {
	Item                   *models.Item              `json:"item"`
	Links                  services.EntityLinks      `json:"links"`
	LinkTypes              []models.LinkType         `json:"link_types"`
	RequestTypeFields      []models.RequestTypeField `json:"request_type_fields"`
	Transitions            ItemTransitionSummary     `json:"transitions"`
	Watching               bool                      `json:"watching"`
	Children               []models.Item             `json:"children"`
	Ancestors              []models.Item             `json:"ancestors"`
	CurrentItemType        *services.ItemTypeResult  `json:"current_item_type"`
	CurrentHierarchyLevel  *models.HierarchyLevel    `json:"current_hierarchy_level"`
	AvailableSubIssueTypes []services.ItemTypeResult `json:"available_sub_issue_types"`
	Priorities             []models.PriorityDisplay  `json:"priorities"`
	ScreenContext          ItemDetailScreenContext   `json:"screen_context"`
	ManualActions          []*models.Action          `json:"manual_actions"`
	PersonalTaskCount      int                       `json:"personal_task_count"`
	SCMAvailable           bool                      `json:"scm_available"`
	HasAgentRuns           bool                      `json:"has_agent_runs"`
}

func NewItemDetailHandler(
	item *ItemHandler,
	links *ItemLinkHandler,
	linkTypes *LinkTypeHandler,
	screens *ScreenHandler,
	requestTypes *RequestTypeHandler,
	actions *ActionsHandler,
) *ItemDetailHandler {
	return &ItemDetailHandler{
		item: item, links: links, linkTypes: linkTypes, screens: screens,
		requestTypes: requestTypes, actions: actions,
	}
}

func (h *ItemDetailHandler) Get(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	h.respond(w, r, itemID)
}

func (h *ItemDetailHandler) GetByKeyAndNumber(w http.ResponseWriter, r *http.Request) {
	// Authenticate before resolving the stable key so anonymous callers cannot
	// turn this route into an unauthenticated database lookup.
	if _, ok := RequireAuth(w, r); !ok {
		return
	}
	itemID, err := h.resolveKeyAndNumber(r.PathValue("key"), r.PathValue("number"))
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "item")
		return
	}
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}
	h.respond(w, r, itemID)
}

func (h *ItemDetailHandler) respond(w http.ResponseWriter, r *http.Request, itemID int) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	item, err := h.item.loadItemForUser(r.Context(), user, itemID, true)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "item")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	response := h.load(r.Context(), user, item, r.URL.Query().Get("surface"))
	respondJSONOK(w, response)
}

func (h *ItemDetailHandler) load(ctx context.Context, user *models.User, item *models.Item, surface string) ItemDetailSummaryResponse {
	response := ItemDetailSummaryResponse{
		Item: item,
		Links: services.EntityLinks{
			Outgoing: []models.ItemLink{}, Incoming: []models.ItemLink{},
		},
		LinkTypes:              []models.LinkType{},
		RequestTypeFields:      []models.RequestTypeField{},
		Transitions:            ItemTransitionSummary{AvailableTransitions: []map[string]any{}},
		Children:               []models.Item{},
		Ancestors:              []models.Item{},
		AvailableSubIssueTypes: []services.ItemTypeResult{},
		Priorities:             []models.PriorityDisplay{},
		ManualActions:          []*models.Action{},
	}

	// The primary item endpoint intentionally permits an active approver to see
	// an item without workspace item.view. Auxiliary endpoints do not. Preserve
	// that boundary by returning only the item for that narrow actor path.
	canViewWorkspace, err := h.item.canViewItem(user.ID, item.WorkspaceID)
	if err != nil {
		slog.Warn("item detail summary: permission lookup failed", "item_id", item.ID, "error", err)
		return response
	}
	if !canViewWorkspace {
		return response
	}

	mobile := strings.EqualFold(surface, "mobile")
	var wait sync.WaitGroup
	run := func(name string, load func() error) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := load(); err != nil {
				slog.Warn("item detail summary: section unavailable", "section", name, "item_id", item.ID, "error", err)
			}
		}()
	}

	run("transitions", func() error {
		transitions, err := h.item.loadAvailableStatusTransitions(ctx, user.ID, item)
		if err == nil {
			response.Transitions = transitions
		}
		return err
	})
	run("watch", func() error {
		watching, err := h.item.itemRepo.IsWatching(user.ID, item.ID)
		if err == nil {
			response.Watching = watching
		}
		return err
	})
	run("children", func() error {
		children, err := h.loadRelation(ctx, user.ID, item.ID, false)
		if err == nil {
			response.Children = children
		}
		return err
	})
	if item.ParentID != nil {
		run("ancestors", func() error {
			ancestors, err := h.loadRelation(ctx, user.ID, item.ID, true)
			if err == nil {
				response.Ancestors = ancestors
			}
			return err
		})
	}
	run("type context", func() error {
		itemTypes, hierarchyLevels, err := h.loadTypeContext()
		if err == nil {
			if item.ItemTypeID != nil {
				for i := range itemTypes {
					if itemTypes[i].ID == *item.ItemTypeID {
						selected := itemTypes[i]
						response.CurrentItemType = &selected
						break
					}
				}
			}
			if response.CurrentItemType != nil {
				if response.CurrentItemType.HierarchyLevel == models.HierarchyLevelGenericSubtask {
					return nil
				}
				for i := range hierarchyLevels {
					if hierarchyLevels[i].Level == response.CurrentItemType.HierarchyLevel {
						selected := hierarchyLevels[i]
						response.CurrentHierarchyLevel = &selected
						break
					}
				}
				if response.CurrentHierarchyLevel != nil {
					next := response.CurrentHierarchyLevel.Level + 1
					for _, itemType := range itemTypes {
						if itemType.HierarchyLevel == next ||
							itemType.HierarchyLevel == models.HierarchyLevelGenericSubtask {
							response.AvailableSubIssueTypes = append(response.AvailableSubIssueTypes, itemType)
						}
					}
				}
			}
		}
		return err
	})

	if mobile {
		run("personal task count", func() error {
			count, err := h.loadPersonalTaskCount(user.ID, item.ID)
			if err == nil {
				response.PersonalTaskCount = count
			}
			return err
		})
		run("panel availability", func() error {
			scmAvailable, hasAgentRuns, err := h.loadPanelAvailability(item)
			if err == nil {
				response.SCMAvailable = scmAvailable
				response.HasAgentRuns = hasAgentRuns
			}
			return err
		})
	} else {
		run("links", func() error {
			outgoing, incoming, err := h.links.linkSvc.ListLinksForEntityWithChecks(user.ID, "item", item.ID)
			if err == nil {
				if outgoing == nil {
					outgoing = []models.ItemLink{}
				}
				if incoming == nil {
					incoming = []models.ItemLink{}
				}
				response.Links = services.EntityLinks{Outgoing: outgoing, Incoming: incoming}
			}
			return err
		})
		run("link types", func() error {
			linkTypes, err := h.linkTypes.repo.List(false)
			if err == nil {
				if linkTypes == nil {
					linkTypes = []models.LinkType{}
				}
				response.LinkTypes = linkTypes
			}
			return err
		})
		if item.RequestTypeID != nil {
			run("request type fields", func() error {
				fields, err := h.loadRequestTypeFields(ctx, user.ID, *item.RequestTypeID)
				if err == nil {
					response.RequestTypeFields = fields
				}
				return err
			})
		}
		run("configuration", func() error {
			priorities, screens, err := h.loadConfigurationContext(item)
			if err == nil {
				response.Priorities = priorities
				response.ScreenContext = screens
			}
			return err
		})
		run("manual actions", func() error {
			actions, err := h.loadManualActions(user.ID, item.WorkspaceID)
			if err == nil {
				response.ManualActions = actions
			}
			return err
		})
	}

	wait.Wait()
	return response
}

func (h *ItemDetailHandler) resolveKeyAndNumber(key, itemRef string) (int, error) {
	key = strings.TrimSpace(key)
	itemRef = strings.TrimSpace(itemRef)
	if key == "" || itemRef == "" {
		return 0, errors.New("workspace key and item number are required")
	}
	lookupKey := key
	if parts := strings.SplitN(itemRef, "-", 2); len(parts) == 2 {
		if _, numericKey := strconv.Atoi(key); numericKey != nil && !strings.EqualFold(key, parts[0]) {
			return 0, repository.ErrNotFound
		}
		lookupKey = parts[0]
		itemRef = parts[1]
	}
	number, err := strconv.Atoi(itemRef)
	if err != nil || number <= 0 {
		return 0, errors.New("invalid item number")
	}
	return repository.NewItemRepository(h.item.db).FindIDByKeyAndNumber(lookupKey, number)
}

func (h *ItemDetailHandler) loadRelation(ctx context.Context, userID, itemID int, ancestors bool) ([]models.Item, error) {
	var items []models.Item
	var err error
	if ancestors {
		items, err = h.item.hierarchyService.GetAncestorsContext(ctx, itemID)
	} else {
		items, err = h.item.hierarchyService.GetChildrenContext(ctx, itemID)
	}
	if err != nil {
		return nil, err
	}
	items, err = h.item.filterItemsByPermissions(userID, items)
	if err != nil {
		return nil, err
	}
	if err := repository.NewLabelRepository(h.item.db).LoadForItemsContext(ctx, items); err != nil {
		return nil, err
	}
	if err := LoadPersonalLabelsForItemsContext(ctx, h.item.db, items, userID); err != nil {
		return nil, err
	}
	if err := repository.NewMilestoneAttachRepository(h.item.db).LoadForItemsContext(ctx, items); err != nil {
		return nil, err
	}
	if items == nil {
		items = []models.Item{}
	}
	return items, nil
}

func (h *ItemDetailHandler) loadTypeContext() ([]services.ItemTypeResult, []models.HierarchyLevel, error) {
	itemTypes, err := services.NewConfigReadService(h.item.db).ListItemTypes()
	if err != nil {
		return nil, nil, err
	}
	entities, err := services.NewEnumService(h.item.db, services.NewHierarchyLevelConfig()).GetAll()
	if err != nil {
		return nil, nil, err
	}
	levels := make([]models.HierarchyLevel, 0, len(entities))
	for _, entity := range entities {
		level, ok := entity.(*models.HierarchyLevel)
		if !ok {
			return nil, nil, fmt.Errorf("unexpected hierarchy level type %T", entity)
		}
		levels = append(levels, *level)
	}
	return itemTypes, levels, nil
}

func (h *ItemDetailHandler) loadPersonalTaskCount(userID, itemID int) (int, error) {
	personalWorkspaceID, err := repository.NewWorkspaceRepository(h.item.db).GetActivePersonalWorkspaceID(userID)
	if errors.Is(err, repository.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	items, err := repository.NewItemRepository(h.item.db).ListRelatedPersonalItems(itemID, personalWorkspaceID)
	return len(items), err
}

func (h *ItemDetailHandler) loadPanelAvailability(item *models.Item) (scmAvailable, hasAgentRuns bool, err error) {
	return repository.NewItemRepository(h.item.db).GetDetailPanelAvailability(item.WorkspaceID, item.ID)
}

func (h *ItemDetailHandler) loadRequestTypeFields(ctx context.Context, userID, requestTypeID int) ([]models.RequestTypeField, error) {
	requestType, err := h.requestTypes.repo.GetByID(requestTypeID)
	if err != nil {
		return nil, err
	}
	canManage, err := h.requestTypes.channelService.UserCanManage(ctx, userID, requestType.ChannelID)
	if err != nil || !canManage {
		return []models.RequestTypeField{}, err
	}
	fields, err := h.requestTypes.repo.ListFields(requestTypeID)
	if fields == nil {
		fields = []models.RequestTypeField{}
	}
	return fields, err
}

func (h *ItemDetailHandler) loadConfigurationContext(item *models.Item) ([]models.PriorityDisplay, ItemDetailScreenContext, error) {
	repo := repository.NewConfigurationSetRepository(h.item.db)
	configSetID, err := repo.GetWorkspaceConfigSetID(item.WorkspaceID)
	if err != nil {
		return nil, ItemDetailScreenContext{}, err
	}
	var configSet *models.ConfigurationSet
	if configSetID != nil {
		configSet, err = repo.FindByID(*configSetID)
		if err != nil {
			return nil, ItemDetailScreenContext{}, err
		}
	}

	editID := resolveItemDetailScreenID(configSet, item.ItemTypeID, "edit", 1)
	viewID := resolveItemDetailScreenID(configSet, item.ItemTypeID, "view", 1)
	editScreen, err := h.screens.loadScreen(editID)
	if err != nil {
		return nil, ItemDetailScreenContext{}, err
	}
	screens := ItemDetailScreenContext{Edit: editScreen}
	if viewID != editID {
		viewScreen, loadErr := h.screens.loadScreen(viewID)
		if loadErr != nil {
			return nil, ItemDetailScreenContext{}, loadErr
		}
		screens.View = viewScreen
	}
	priorities := []models.PriorityDisplay{}
	if configSet != nil && len(configSet.PrioritiesDetailed) > 0 {
		priorities = configSet.PrioritiesDetailed
	}
	return priorities, screens, nil
}

func resolveItemDetailScreenID(configSet *models.ConfigurationSet, itemTypeID *int, mode string, fallback int) int {
	if configSet == nil {
		return fallback
	}
	if itemTypeID != nil {
		for _, config := range configSet.ItemTypeConfigs {
			if config.ItemTypeID != *itemTypeID {
				continue
			}
			if screenID := itemTypeConfigScreenID(config, mode); screenID != nil {
				return *screenID
			}
			if mode != "create" && config.CreateScreenID != nil {
				return *config.CreateScreenID
			}
			break
		}
	}
	if screenID := configurationSetScreenID(configSet, mode); screenID != nil {
		return *screenID
	}
	if mode != "create" && configSet.CreateScreenID != nil {
		return *configSet.CreateScreenID
	}
	return fallback
}

func itemTypeConfigScreenID(config models.ItemTypeConfig, mode string) *int {
	switch mode {
	case "create":
		return config.CreateScreenID
	case "edit":
		return config.EditScreenID
	case "view":
		return config.ViewScreenID
	default:
		return nil
	}
}

func configurationSetScreenID(config *models.ConfigurationSet, mode string) *int {
	switch mode {
	case "create":
		return config.CreateScreenID
	case "edit":
		return config.EditScreenID
	case "view":
		return config.ViewScreenID
	default:
		return nil
	}
}

func (h *ItemDetailHandler) loadManualActions(userID, workspaceID int) ([]*models.Action, error) {
	if h.actions == nil || h.actions.permissionService == nil {
		return []*models.Action{}, nil
	}
	actions, err := h.actions.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	manual := make([]*models.Action, 0)
	for _, action := range actions {
		if !action.IsEnabled || action.TriggerType != models.ActionTriggerManual {
			continue
		}
		allowed, err := h.actions.canTriggerManualAction(userID, workspaceID, action)
		if err != nil {
			return nil, err
		}
		if allowed {
			manual = append(manual, action)
		}
	}
	return manual, nil
}
