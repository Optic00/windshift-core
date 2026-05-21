package services

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"windshift/internal/models"
)

// pageChunkMaxBytes caps the byte size of a single chunk. Sections larger
// than this are split on paragraph boundaries so search snippets stay
// focused and future embedding models don't hit context limits.
const pageChunkMaxBytes = 2048

// pageChunkMinBytes keeps tiny trailing fragments from becoming their own
// chunk. Anything below this is appended to the previous chunk instead of
// emitted standalone.
const pageChunkMinBytes = 128

// chunkPageMarkdown splits Markdown source into ordered chunks suitable for
// full-text search snippets and (future) embeddings. The splitter is
// deterministic and never panics — invalid Markdown just produces fewer,
// larger chunks. byte_start/byte_end are offsets into the original source.
//
// Splitting strategy:
//
//  1. Walk the source line-by-line, tracking current heading_path
//     (e.g. "Top > Sub").
//  2. Whenever a new ATX heading (#, ##, ...) appears, close the current
//     section and start a new one.
//  3. If a section exceeds pageChunkMaxBytes, break it on the previous
//     blank line; if there isn't one, hard-wrap at the limit.
//  4. Trailing fragments under pageChunkMinBytes merge into the prior
//     chunk to avoid scattershot single-line entries.
func chunkPageMarkdown(content string) []chunkSpec {
	if content == "" {
		return nil
	}

	var sections []chunkSpec
	var (
		curHeadingStack = make([]string, 6)
		curStart        int
		curBuilder      strings.Builder
	)

	flush := func(end int) {
		if curBuilder.Len() == 0 {
			return
		}
		text := strings.TrimRight(curBuilder.String(), "\n")
		if text == "" {
			curBuilder.Reset()
			return
		}
		sections = append(sections, chunkSpec{
			HeadingPath: buildHeadingPath(curHeadingStack),
			Content:     text,
			ByteStart:   curStart,
			ByteEnd:     end,
		})
		curBuilder.Reset()
	}

	cursor := 0
	for cursor < len(content) {
		nl := strings.IndexByte(content[cursor:], '\n')
		var line string
		var lineEnd int
		if nl < 0 {
			line = content[cursor:]
			lineEnd = len(content)
		} else {
			line = content[cursor : cursor+nl]
			lineEnd = cursor + nl + 1
		}

		level, heading := atxHeading(line)
		if level > 0 && level <= len(curHeadingStack) {
			flush(cursor)
			for i := level - 1; i < len(curHeadingStack); i++ {
				curHeadingStack[i] = ""
			}
			curHeadingStack[level-1] = heading
			curStart = cursor
		}

		if curBuilder.Len() == 0 {
			curStart = cursor
		}
		curBuilder.WriteString(line)
		curBuilder.WriteByte('\n')
		cursor = lineEnd
	}
	flush(cursor)

	// Post-pass: split oversize sections, merge undersize trailers.
	return rebalanceChunks(sections)
}

type chunkSpec struct {
	HeadingPath string
	Content     string
	ByteStart   int
	ByteEnd     int
}

func atxHeading(line string) (level int, text string) {
	trim := strings.TrimLeft(line, " ")
	if !strings.HasPrefix(trim, "#") {
		return 0, ""
	}
	i := 0
	for i < len(trim) && trim[i] == '#' && i < 6 {
		i++
	}
	if i == 0 || i >= len(trim) {
		return 0, ""
	}
	if trim[i] != ' ' && trim[i] != '\t' {
		return 0, ""
	}
	return i, strings.TrimSpace(trim[i+1:])
}

func buildHeadingPath(stack []string) string {
	parts := make([]string, 0, len(stack))
	for _, h := range stack {
		if h == "" {
			continue
		}
		parts = append(parts, h)
	}
	return strings.Join(parts, " > ")
}

func rebalanceChunks(in []chunkSpec) []chunkSpec {
	var out []chunkSpec
	for _, c := range in {
		if len(c.Content) <= pageChunkMaxBytes {
			out = append(out, c)
			continue
		}
		out = append(out, splitOversizeChunk(c)...)
	}

	// Merge undersize trailing chunk into its predecessor so single-line
	// stragglers don't become their own row.
	if len(out) >= 2 {
		last := out[len(out)-1]
		prev := out[len(out)-2]
		if len(last.Content) < pageChunkMinBytes && last.HeadingPath == prev.HeadingPath {
			merged := chunkSpec{
				HeadingPath: prev.HeadingPath,
				Content:     prev.Content + "\n\n" + last.Content,
				ByteStart:   prev.ByteStart,
				ByteEnd:     last.ByteEnd,
			}
			out = append(out[:len(out)-2], merged)
		}
	}
	return out
}

func splitOversizeChunk(c chunkSpec) []chunkSpec {
	var out []chunkSpec
	body := c.Content
	startOffset := c.ByteStart
	for len(body) > pageChunkMaxBytes {
		// Prefer breaking at the last blank line before the limit; fall
		// back to last newline; finally hard-wrap at the byte limit.
		cut := strings.LastIndex(body[:pageChunkMaxBytes], "\n\n")
		if cut < 0 {
			cut = strings.LastIndex(body[:pageChunkMaxBytes], "\n")
		}
		if cut < pageChunkMinBytes {
			cut = pageChunkMaxBytes
		}
		piece := strings.TrimRight(body[:cut], "\n")
		out = append(out, chunkSpec{
			HeadingPath: c.HeadingPath,
			Content:     piece,
			ByteStart:   startOffset,
			ByteEnd:     startOffset + cut,
		})
		body = body[cut:]
		startOffset += cut
		// Skip leading newlines on the continuation so the next chunk
		// starts on real content.
		body = strings.TrimLeft(body, "\n")
	}
	if strings.TrimSpace(body) != "" {
		out = append(out, chunkSpec{
			HeadingPath: c.HeadingPath,
			Content:     body,
			ByteStart:   startOffset,
			ByteEnd:     c.ByteEnd,
		})
	}
	return out
}

// buildPageChunks converts chunkSpecs into model rows ready for
// PageRepository.InsertChunkTx. revisionNumber is the revision_number on the
// page row at the moment the chunks were produced so search results can
// reference a specific snapshot.
func buildPageChunks(page *models.Page, revisionNumber int, specs []chunkSpec) []models.PageChunk {
	out := make([]models.PageChunk, 0, len(specs))
	for i, s := range specs {
		hash := sha256.Sum256([]byte(s.Content))
		out = append(out, models.PageChunk{
			PageID:         page.ID,
			WorkspaceID:    page.WorkspaceID,
			RevisionNumber: revisionNumber,
			Position:       i,
			HeadingPath:    s.HeadingPath,
			Content:        s.Content,
			TokenCount:     estimateTokens(s.Content),
			ByteStart:      s.ByteStart,
			ByteEnd:        s.ByteEnd,
			ContentHash:    hex.EncodeToString(hash[:]),
		})
	}
	return out
}

// estimateTokens is a rough token estimate; precision matters only for
// pgvector budgeting which is not enabled in Phase 1. ~4 bytes/token is a
// reasonable average for English Markdown.
func estimateTokens(content string) int {
	if content == "" {
		return 0
	}
	return (len(content) + 3) / 4
}
