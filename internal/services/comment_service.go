package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
)

// WebhookDispatcher is an interface for dispatching webhook events.
// This avoids an import cycle with the webhook package.
type WebhookDispatcher interface {
	DispatchEvent(eventType string, item *models.Item)
}

// EmailReplyHandler is an interface for handling outbound email replies on comment creation.
// This avoids an import cycle with the email reply service.
type EmailReplyHandler interface {
	HandleCommentCreated(params HandleCommentParams) error
}

// HandleCommentParams contains the parameters for handling a comment creation event.
type HandleCommentParams struct {
	CommentID        int
	ItemID           int
	AuthorID         int
	PortalCustomerID *int
	Content          string
	IsPrivate        bool
}

// AgentMentionTrigger is the coding-agent harness's interest in new
// comments (WI-264): @mentions of a binding's acting user start a run on
// the commented item. Kept as an interface so CommentService stays
// decoupled from BindingService (which satisfies it) and tests can stub it.
type AgentMentionTrigger interface {
	MaybeStartRunsForMentions(ctx context.Context, workspaceID, itemID int, mentionedUserIDs []int, commentAuthorID int, commentBody string, commentID int) error
}

// CommentService encapsulates comment creation logic used by both HTTP handlers
// and action automation service.
type CommentService struct {
	db                  database.Database
	activityTracker     *ActivityTracker
	notificationService *NotificationService
	mentionService      *MentionService
	webhookSender       WebhookDispatcher
	emailReplyService   EmailReplyHandler
	agentMentionTrigger AgentMentionTrigger
}

// CreateCommentParams contains the parameters for creating a comment.
type CreateCommentParams struct {
	ItemID                int
	AuthorID              int        // Internal user (0 if portal customer without linked user)
	PortalCustomerID      *int       // Portal customer (nil if internal user)
	Content               string     // Raw content (will be sanitized)
	IsPrivate             bool       // For action automation private notes
	ActorUserID           int        // User performing the action (for notifications, 0 for portal customers)
	CreatedAt             *time.Time // Optional: override created_at (e.g. for imports preserving original timestamps)
	UpdatedAt             *time.Time // Optional: override updated_at (imports preserving original timestamps); defaults to created_at
	SuppressNotifications bool       // Skip notifications, mentions, webhooks, and email replies (e.g. plugin-created comments)
}

// CreateCommentResult contains the result of creating a comment.
type CreateCommentResult struct {
	Comment   *models.Comment
	CommentID int64
}

// NewCommentService creates a new CommentService.
func NewCommentService(db database.Database) *CommentService {
	return &CommentService{
		db: db,
	}
}

// SetActivityTracker sets the activity tracker for tracking comment activity.
func (s *CommentService) SetActivityTracker(tracker *ActivityTracker) {
	s.activityTracker = tracker
}

// SetNotificationService sets the notification service for emitting comment events.
func (s *CommentService) SetNotificationService(ns *NotificationService) {
	s.notificationService = ns
}

// SetMentionService sets the mention service for processing @mentions.
func (s *CommentService) SetMentionService(ms *MentionService) {
	s.mentionService = ms
}

// SetWebhookSender sets the webhook sender for dispatching webhook events.
func (s *CommentService) SetWebhookSender(ws WebhookDispatcher) {
	s.webhookSender = ws
}

// SetEmailReplyService sets the email reply service for sending threaded replies to portal customers.
func (s *CommentService) SetEmailReplyService(ers EmailReplyHandler) {
	s.emailReplyService = ers
}

// SetAgentMentionTrigger wires the optional coding-agent @mention trigger
// (WI-264). Nil disables the hook; Create calls it once per created comment.
func (s *CommentService) SetAgentMentionTrigger(trigger AgentMentionTrigger) {
	s.agentMentionTrigger = trigger
}

// All comment writes use CommentService so side effects and post-commit item changes cannot be bypassed.
const (
	insertCommentAuthorSQL = `INSERT INTO comments (item_id, author_id, content, is_private, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING id`
	insertCommentPortalSQL = `INSERT INTO comments (item_id, portal_customer_id, content, is_private, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING id`
	updateCommentSQL       = `UPDATE comments SET content = ?, updated_at = ? WHERE id = ?`
	deleteCommentSQL       = `DELETE FROM comments WHERE id = ?`
)

// CreateInTx inserts a comment row inside an existing transaction and returns
// its id, with NO side-effects (no notifications, activity bump, or publish).
// Callers that must write a comment atomically with other rows — GitHub issue
// sync writes its tracking rows in the same tx — use this and publish
// themselves after they commit. authorID == 0 inserts a system (NULL author)
// comment.
func (s *CommentService) CreateInTx(ctx context.Context, tx database.Tx, itemID, authorID int, content string, createdAt time.Time) (int64, error) {
	var author interface{}
	if authorID != 0 {
		author = authorID
	}
	var id int64
	err := tx.QueryRowContext(ctx, insertCommentAuthorSQL, itemID, author, sanitize.Comment.Sanitize(content), false, createdAt, createdAt).Scan(&id)
	return id, err
}

// UpdateContentInTx updates a comment's content inside an existing transaction,
// with no side-effects/publish (the caller publishes after it commits).
func (s *CommentService) UpdateContentInTx(ctx context.Context, tx database.Tx, commentID int, content string, updatedAt time.Time) error {
	_, err := tx.ExecContext(ctx, updateCommentSQL, sanitize.Comment.Sanitize(content), updatedAt, commentID)
	return err
}

func (s *CommentService) Create(params CreateCommentParams) (*CreateCommentResult, error) {
	// 1. Sanitize content (XSS prevention — strips HTML tags + dangerous Markdown URLs)
	sanitizedContent := sanitize.Comment.Sanitize(params.Content)

	// 2. Get item details for notifications and the webhook payload
	item, err := repository.NewItemRepository(s.db).FindByIDWithDetails(params.ItemID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("item not found: %d", params.ItemID)
		}
		return nil, fmt.Errorf("failed to fetch item details: %w", err)
	}

	// 3. Insert into DB
	now := time.Now()
	if params.CreatedAt != nil {
		now = *params.CreatedAt
	}
	updatedAt := now
	if params.UpdatedAt != nil {
		updatedAt = *params.UpdatedAt
	}

	var commentID int64
	if params.PortalCustomerID != nil && params.AuthorID == 0 {
		// Portal customer without linked user — insert with portal_customer_id
		err = s.db.QueryRow(insertCommentPortalSQL,
			params.ItemID, *params.PortalCustomerID, sanitizedContent, params.IsPrivate, now, updatedAt).Scan(&commentID)
	} else {
		// Internal user or portal customer with linked user; AuthorID == 0 with no
		// portal customer inserts a system (NULL author) comment.
		var authorID interface{}
		if params.AuthorID != 0 {
			authorID = params.AuthorID
		}
		err = s.db.QueryRow(insertCommentAuthorSQL,
			params.ItemID, authorID, sanitizedContent, params.IsPrivate, now, updatedAt).Scan(&commentID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create comment: %w", err)
	}

	// Comments update Bubble Mode recency best-effort at this shared entry point.
	if err := repository.NewItemRepository(s.db).TouchActivity(s.db, params.ItemID, now); err != nil {
		slog.Warn("failed to bump item last_active_at on comment",
			slog.String("component", "comment_service"),
			slog.Int("item_id", params.ItemID),
			slog.Any("error", err),
		)
	}

	// 4. Track activity (if activityTracker != nil)
	if s.activityTracker != nil {
		if err := s.activityTracker.TrackItemActivity(params.ActorUserID, params.ItemID, ActivityComment); err != nil {
			// Don't fail the request if activity tracking fails, just log it
			slog.Warn("failed to track comment activity",
				slog.String("component", "comment_service"),
				slog.Int("item_id", params.ItemID),
				slog.Any("error", err),
			)
		}
	}

	// Steps 5-8: notifications, mentions, webhooks, and email replies (skipped when SuppressNotifications is true)
	if !params.SuppressNotifications {
		// Idempotently follow internal commenters for reply notifications. Portal
		// customers have no user watch; agent recipients are filtered downstream.
		if s.activityTracker != nil && params.ActorUserID > 0 {
			if err := s.activityTracker.AddWatch(params.ActorUserID, params.ItemID, "Commented on item"); err != nil {
				slog.Warn("failed to auto-subscribe commenter to item",
					slog.String("component", "comment_service"),
					slog.Int("item_id", params.ItemID),
					slog.Int("actor_user_id", params.ActorUserID),
					slog.Any("error", err),
				)
			}
		}

		// 5. Emit notification event (if notificationService != nil)
		if s.notificationService != nil {
			// Get actor name for notification
			var actorName string
			if params.PortalCustomerID != nil {
				// Portal customer — look up customer name
				if err := s.db.QueryRow("SELECT name FROM portal_customers WHERE id = ?", *params.PortalCustomerID).Scan(&actorName); err != nil {
					slog.Warn("failed to look up portal customer name", slog.Any("error", err), slog.Int("portal_customer_id", *params.PortalCustomerID))
				}
				if actorName == "" {
					actorName = "Portal Customer"
				}
			} else {
				if err := s.db.QueryRow("SELECT username FROM users WHERE id = ?", params.ActorUserID).Scan(&actorName); err != nil {
					slog.Warn("failed to look up username", slog.Any("error", err), slog.Int("user_id", params.ActorUserID))
				}
				if actorName == "" {
					actorName = fmt.Sprintf("User #%d", params.ActorUserID)
				}
			}

			// Construct the item key (e.g., "TST-1")
			itemKey := fmt.Sprintf("%s-%d", item.WorkspaceKey, item.WorkspaceItemNumber)

			slog.Debug("emitting notification event for comment",
				slog.String("component", "comment_service"),
				slog.Int("item_id", params.ItemID),
				slog.Int("actor_user_id", params.ActorUserID),
			)

			s.notificationService.EmitEvent(&NotificationEvent{
				EventType:   models.EventCommentCreated,
				WorkspaceID: item.WorkspaceID,
				ActorUserID: params.ActorUserID,
				ItemID:      params.ItemID,
				AssigneeID:  item.AssigneeID,
				CreatorID:   item.CreatorID,
				Title:       "New Comment Added",
				TemplateData: map[string]interface{}{
					"item.title": item.Title,
					"item.key":   itemKey,
					"item.id":    params.ItemID,
					"user.name":  actorName,
				},
			})
		}

		// 6. Process @mentions (if mentionService != nil)
		if s.mentionService != nil {
			if err := s.mentionService.ProcessMentions(ProcessMentionsParams{
				SourceType:  "comment",
				SourceID:    int(commentID),
				Content:     params.Content, // Use original content for mention parsing
				ItemID:      params.ItemID,
				WorkspaceID: item.WorkspaceID,
				ActorUserID: params.ActorUserID,
			}); err != nil {
				slog.Warn("failed to process mentions",
					slog.String("component", "comment_service"),
					slog.Int64("comment_id", commentID),
					slog.Any("error", err),
				)
				// Don't fail the request if mention processing fails
			}
		}

		// Create-only agent mention triggers require an internal credential principal;
		// logged trigger failures never block comments.
		if s.agentMentionTrigger != nil && s.mentionService != nil && params.ActorUserID > 0 {
			if ids, err := s.mentionService.ResolveMentionedUserIDs(params.Content); err != nil {
				slog.Warn("failed to resolve mentions for agent trigger",
					slog.String("component", "comment_service"),
					slog.Int64("comment_id", commentID),
					slog.Any("error", err),
				)
			} else if len(ids) > 0 {
				// Admission outlives client disconnects; use stored sanitized content so
				// agent instructions cannot diverge from the visible comment.
				if err := s.agentMentionTrigger.MaybeStartRunsForMentions(context.Background(), item.WorkspaceID, params.ItemID, ids, params.ActorUserID, sanitizedContent, int(commentID)); err != nil {
					slog.Warn("coding-agent mention trigger failed",
						slog.String("component", "comment_service"),
						slog.Int("item_id", params.ItemID),
						slog.Int64("comment_id", commentID),
						slog.Any("error", err),
					)
				}
			}
		}

		// 7. Dispatch webhook (if webhookSender != nil)
		if s.webhookSender != nil {
			s.webhookSender.DispatchEvent("comment.created", item)
		}

		// 8. Handle outbound email reply (if emailReplyService != nil)
		if s.emailReplyService != nil {
			if err := s.emailReplyService.HandleCommentCreated(HandleCommentParams{
				CommentID:        int(commentID),
				ItemID:           params.ItemID,
				AuthorID:         params.AuthorID,
				PortalCustomerID: params.PortalCustomerID,
				Content:          sanitizedContent,
				IsPrivate:        params.IsPrivate,
			}); err != nil {
				slog.Warn("failed to handle email reply for comment",
					slog.String("component", "comment_service"),
					slog.Int64("comment_id", commentID),
					slog.Any("error", err),
				)
			}
		}
	}

	// Live-update publish (WI-483): the comment row is committed. Refresh the
	// item's comment list for anyone viewing it.
	PublishItemChange(params.ItemID, ItemChangeComment)

	// 9. Return created comment
	return &CreateCommentResult{
		CommentID: commentID,
	}, nil
}

// CommentWithDetails contains a comment with its related details
type CommentWithDetails struct {
	models.Comment
	WorkspaceID int
	ItemTitle   string
}

// Get retrieves a comment by ID with author details
func (s *CommentService) Get(commentID int) (*CommentWithDetails, error) {
	var comment CommentWithDetails
	var authorID, portalCustomerID sql.NullInt64
	var authorName, authorEmail, authorAvatar sql.NullString

	err := s.db.QueryRow(`
		SELECT c.id, c.item_id, c.author_id, c.portal_customer_id, c.content, c.is_private,
		       c.created_at AS feed_created_at, c.updated_at,
		       COALESCE(NULLIF(TRIM(COALESCE(u.first_name, '') || ' ' || COALESCE(u.last_name, '')), ''), pc.name) AS author_name,
		       COALESCE(u.email, pc.email) AS author_email,
		       u.avatar_url, 'human' AS source, COALESCE(u.is_agent, FALSE) AS is_agent,
		       i.workspace_id, i.title
		FROM comments c
		LEFT JOIN users u ON c.author_id = u.id
		LEFT JOIN portal_customers pc ON c.portal_customer_id = pc.id
		JOIN items i ON c.item_id = i.id
		WHERE c.id = ?
	`, commentID).Scan(
		&comment.ID, &comment.ItemID, &authorID, &portalCustomerID, &comment.Content, &comment.IsPrivate,
		&comment.CreatedAt, &comment.UpdatedAt,
		&authorName, &authorEmail, &authorAvatar, &comment.Source, &comment.IsAgent,
		&comment.WorkspaceID, &comment.ItemTitle,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("comment %d: %w", commentID, repository.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch comment: %w", err)
	}

	if authorID.Valid {
		id := int(authorID.Int64)
		comment.AuthorID = &id
	}
	if portalCustomerID.Valid {
		id := int(portalCustomerID.Int64)
		comment.PortalCustomerID = &id
	}
	if authorName.Valid {
		comment.AuthorName = authorName.String
	}
	if authorEmail.Valid {
		comment.AuthorEmail = authorEmail.String
	}
	if authorAvatar.Valid {
		comment.AuthorAvatar = authorAvatar.String
	}

	return &comment, nil
}

// GetFeedByItemID returns the cookie-auth comment feed for an item. In
// addition to ordinary comments it projects approval decision comments into
// the same model so every HTTP surface reads comment data through this
// service. Agent-owner attribution is permission-filtered by the caller.
func (s *CommentService) GetFeedByItemID(itemID int, includeAgentOwner bool) ([]models.Comment, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.item_id, c.author_id, c.portal_customer_id, c.content, c.is_private,
		       c.created_at AS feed_created_at, c.updated_at,
		       COALESCE(NULLIF(TRIM(COALESCE(u.first_name, '') || ' ' || COALESCE(u.last_name, '')), ''), pc.name, 'Unknown User') AS author_name,
		       COALESCE(u.email, pc.email) AS author_email, u.avatar_url,
		       'human' AS source, COALESCE(u.is_agent, FALSE) AS is_agent,
		       COALESCE(NULLIF(TRIM(COALESCE(owner.first_name, '') || ' ' || COALESCE(owner.last_name, '')), ''), owner.username, '') AS agent_owner_name
		FROM comments c
		LEFT JOIN users u ON c.author_id = u.id
		LEFT JOIN users owner ON owner.id = u.agent_owner_user_id
		LEFT JOIN portal_customers pc ON c.portal_customer_id = pc.id
		WHERE c.item_id = ?
		UNION ALL
		SELECT -d.id, ar.item_id, d.actor_user_id, NULL, d.comment, FALSE,
		       d.created_at AS feed_created_at, d.created_at,
		       COALESCE(NULLIF(TRIM(COALESCE(u.first_name, '') || ' ' || COALESCE(u.last_name, '')), ''), 'Unknown User') AS author_name,
		       u.email AS author_email, u.avatar_url,
		       'approval' AS source, COALESCE(u.is_agent, FALSE) AS is_agent,
		       COALESCE(NULLIF(TRIM(COALESCE(owner.first_name, '') || ' ' || COALESCE(owner.last_name, '')), ''), owner.username, '') AS agent_owner_name
		FROM approval_decisions d
		JOIN approval_requests ar ON ar.id = d.approval_request_id
		LEFT JOIN users u ON u.id = d.actor_user_id
		LEFT JOIN users owner ON owner.id = u.agent_owner_user_id
		WHERE ar.item_id = ? AND d.comment IS NOT NULL AND d.comment <> ''
		ORDER BY feed_created_at DESC
	`, itemID, itemID)
	if err != nil {
		return nil, fmt.Errorf("get comment feed for item %d: %w", itemID, err)
	}
	defer func() { _ = rows.Close() }()

	var comments []models.Comment
	for rows.Next() {
		var comment models.Comment
		var authorID, portalCustomerID sql.NullInt64
		var authorName, authorEmail, authorAvatar, agentOwnerName sql.NullString
		if err := rows.Scan(
			&comment.ID, &comment.ItemID, &authorID, &portalCustomerID, &comment.Content, &comment.IsPrivate,
			&comment.CreatedAt, &comment.UpdatedAt, &authorName, &authorEmail, &authorAvatar,
			&comment.Source, &comment.IsAgent, &agentOwnerName,
		); err != nil {
			return nil, fmt.Errorf("scan comment feed for item %d: %w", itemID, err)
		}
		if authorID.Valid {
			id := int(authorID.Int64)
			comment.AuthorID = &id
		}
		if portalCustomerID.Valid {
			id := int(portalCustomerID.Int64)
			comment.PortalCustomerID = &id
		}
		comment.AuthorName = authorName.String
		comment.AuthorEmail = authorEmail.String
		comment.AuthorAvatar = authorAvatar.String
		if includeAgentOwner {
			comment.AgentOwnerName = agentOwnerName.String
		}
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read comment feed for item %d: %w", itemID, err)
	}
	return comments, nil
}

// Update updates a comment's content
func (s *CommentService) Update(commentID int, content string, userID int) (*models.Comment, error) {
	// Sanitize content (strips HTML tags + dangerous Markdown URLs)
	sanitizedContent := sanitize.Comment.Sanitize(content)

	// Check if comment exists. Portal-authored comments have a NULL author_id,
	// so existence must not depend on scanning author_id into a non-null int.
	var exists bool
	err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM comments WHERE id = ?)", commentID).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("failed to check comment: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("comment not found: %d", commentID)
	}

	// Update the comment
	now := time.Now()
	_, err = s.db.ExecWrite(updateCommentSQL, sanitizedContent, now, commentID)
	if err != nil {
		return nil, fmt.Errorf("failed to update comment: %w", err)
	}

	// Fetch and return the updated comment
	var comment models.Comment
	var authorName, authorEmail sql.NullString
	var authorIDNull, portalCustomerID sql.NullInt64

	err = s.db.QueryRow(`
		SELECT c.id, c.item_id, c.author_id, c.portal_customer_id, c.content, c.is_private, c.created_at, c.updated_at,
		       COALESCE(NULLIF(TRIM(COALESCE(u.first_name, '') || ' ' || COALESCE(u.last_name, '')), ''), pc.name) AS author_name,
		       COALESCE(u.email, pc.email) AS author_email
		FROM comments c
		LEFT JOIN users u ON c.author_id = u.id
		LEFT JOIN portal_customers pc ON c.portal_customer_id = pc.id
		WHERE c.id = ?
	`, commentID).Scan(
		&comment.ID, &comment.ItemID, &authorIDNull, &portalCustomerID, &comment.Content, &comment.IsPrivate,
		&comment.CreatedAt, &comment.UpdatedAt,
		&authorName, &authorEmail,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch updated comment: %w", err)
	}

	if authorIDNull.Valid {
		id := int(authorIDNull.Int64)
		comment.AuthorID = &id
	}
	if portalCustomerID.Valid {
		id := int(portalCustomerID.Int64)
		comment.PortalCustomerID = &id
	}
	if authorName.Valid {
		comment.AuthorName = authorName.String
	}
	if authorEmail.Valid {
		comment.AuthorEmail = authorEmail.String
	}

	// Live-update publish (WI-483): the comment edit committed.
	PublishItemChange(comment.ItemID, ItemChangeComment)

	return &comment, nil
}

// Delete removes a comment
func (s *CommentService) Delete(commentID int) error {
	// Capture the parent item id BEFORE the destructive write (this doubles as
	// the existence check) so we can refresh the item's comment list afterwards
	// (WI-483).
	var itemID int
	err := s.db.QueryRow("SELECT item_id FROM comments WHERE id = ?", commentID).Scan(&itemID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("comment not found: %d", commentID)
		}
		return fmt.Errorf("failed to check comment: %w", err)
	}

	_, err = s.db.ExecWrite(deleteCommentSQL, commentID)
	if err != nil {
		return fmt.Errorf("failed to delete comment: %w", err)
	}

	// Live-update publish (WI-483): the delete committed.
	PublishItemChange(itemID, ItemChangeComment)

	return nil
}

// GetByItemID retrieves all comments for an item
func (s *CommentService) GetByItemID(itemID int) ([]models.Comment, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.item_id, c.author_id, c.portal_customer_id, c.content, c.is_private, c.created_at, c.updated_at,
		       COALESCE(NULLIF(TRIM(COALESCE(u.first_name, '') || ' ' || COALESCE(u.last_name, '')), ''), pc.name) AS author_name,
		       COALESCE(u.email, pc.email) AS author_email
		FROM comments c
		LEFT JOIN users u ON c.author_id = u.id
		LEFT JOIN portal_customers pc ON c.portal_customer_id = pc.id
		WHERE c.item_id = ?
		ORDER BY c.created_at DESC
	`, itemID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch comments: %w", err)
	}
	defer rows.Close()

	var comments []models.Comment
	for rows.Next() {
		var c models.Comment
		var authorID, portalCustomerID sql.NullInt64
		var authorName, authorEmail sql.NullString

		err := rows.Scan(
			&c.ID, &c.ItemID, &authorID, &portalCustomerID, &c.Content, &c.IsPrivate,
			&c.CreatedAt, &c.UpdatedAt, &authorName, &authorEmail,
		)
		if err != nil {
			continue
		}

		if authorID.Valid {
			id := int(authorID.Int64)
			c.AuthorID = &id
		}
		if portalCustomerID.Valid {
			id := int(portalCustomerID.Int64)
			c.PortalCustomerID = &id
		}
		if authorName.Valid {
			c.AuthorName = authorName.String
		}
		if authorEmail.Valid {
			c.AuthorEmail = authorEmail.String
		}

		comments = append(comments, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate comments: %w", err)
	}

	if comments == nil {
		comments = []models.Comment{}
	}

	return comments, nil
}

// GetWorkspaceIDForComment returns the workspace ID for a comment's item
func (s *CommentService) GetWorkspaceIDForComment(commentID int) (int, error) {
	var workspaceID int
	err := s.db.QueryRow(`
		SELECT i.workspace_id
		FROM comments c
		JOIN items i ON c.item_id = i.id
		WHERE c.id = ?
	`, commentID).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("comment not found: %d", commentID)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to get workspace ID: %w", err)
	}
	return workspaceID, nil
}

// GetAuthorID returns the internal author ID of a comment. Portal-authored
// comments have no internal author, so they return nil rather than an error.
func (s *CommentService) GetAuthorID(commentID int) (*int, error) {
	var authorID sql.NullInt64
	err := s.db.QueryRow("SELECT author_id FROM comments WHERE id = ?", commentID).Scan(&authorID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("comment not found: %d", commentID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get author ID: %w", err)
	}
	if !authorID.Valid {
		return nil, nil
	}
	id := int(authorID.Int64)
	return &id, nil
}
