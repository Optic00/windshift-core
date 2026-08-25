package handlers

import (
	"fmt"

	"windshift/internal/markdown"
	"windshift/internal/models"
)

func renderCommentMarkdown(comments []models.Comment) error {
	for i := range comments {
		rendered, err := markdown.Render(comments[i].Content)
		if err != nil {
			return fmt.Errorf("render comment %d: %w", comments[i].ID, err)
		}
		comments[i].ContentHTML = rendered
	}
	return nil
}
