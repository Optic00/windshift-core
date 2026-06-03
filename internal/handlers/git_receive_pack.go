package handlers

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// git smart-HTTP push (git-receive-pack) sends its ref-update commands as a
// pkt-line stream ahead of the packfile. The git broker (WI-168) parses this
// command list to enforce the run's per-ref push grant: a run may push only
// the single ref named in grants.Git.Ref, even though the injected SCM
// credential could write any ref in the repo.

const (
	// maxPktLinePayload is git's hard cap on a single pkt-line's payload
	// (0xffff total frame minus the 4-byte length header).
	maxPktLinePayload = 65516
	// maxReceivePackCommands bounds how many ref updates we will parse before
	// giving up — a sane push touches a handful of refs; anything larger is
	// treated as malformed/hostile and rejected (fail closed).
	maxReceivePackCommands = 4096
)

// receivePackCommand is one ref update requested by a push: the ref name and
// the old/new object ids it moves between (a zero new-oid is a delete).
type receivePackCommand struct {
	OldOID string
	NewOID string
	Ref    string
}

// parseReceivePackCommands reads the pkt-line command list at the head of a
// git-receive-pack request body and returns the ref updates it requests. It
// stops at the first flush-pkt (the boundary before the packfile), so the
// caller never has to buffer the pack itself. A body it cannot parse returns
// an error; callers MUST fail closed (reject the push) on any error rather
// than forwarding an unauthorized update.
func parseReceivePackCommands(r io.Reader) ([]receivePackCommand, error) {
	br := bufio.NewReader(r)
	var cmds []receivePackCommand
	for {
		if len(cmds) >= maxReceivePackCommands {
			return nil, fmt.Errorf("receive-pack: too many ref-update commands")
		}
		hdr := make([]byte, 4)
		if _, err := io.ReadFull(br, hdr); err != nil {
			if (err == io.EOF || err == io.ErrUnexpectedEOF) && len(cmds) == 0 {
				return nil, nil // empty body: no ref updates requested
			}
			return nil, fmt.Errorf("receive-pack: read pkt-line length: %w", err)
		}
		n, err := strconv.ParseUint(string(hdr), 16, 32)
		if err != nil {
			return nil, fmt.Errorf("receive-pack: invalid pkt-line length %q", hdr)
		}
		if n == 0 {
			break // flush-pkt: end of the command list
		}
		if n < 4 || n-4 > maxPktLinePayload {
			return nil, fmt.Errorf("receive-pack: pkt-line length %d out of range", n)
		}
		payload := make([]byte, n-4)
		if _, err := io.ReadFull(br, payload); err != nil {
			return nil, fmt.Errorf("receive-pack: read pkt-line payload: %w", err)
		}
		cmd, err := parseReceivePackLine(payload)
		if err != nil {
			return nil, err
		}
		cmds = append(cmds, cmd)
	}
	return cmds, nil
}

// parseReceivePackLine extracts one ref-update command from a pkt-line
// payload of the form "<old-oid> <new-oid> <ref>" — with an optional NUL
// followed by capabilities on the very first command line.
func parseReceivePackLine(payload []byte) (receivePackCommand, error) {
	line := string(payload)
	if i := strings.IndexByte(line, 0); i >= 0 {
		line = line[:i] // drop capabilities advertised after the first ref
	}
	line = strings.TrimRight(line, "\n")
	fields := strings.SplitN(line, " ", 3)
	if len(fields) != 3 || fields[0] == "" || fields[1] == "" || fields[2] == "" {
		return receivePackCommand{}, fmt.Errorf("receive-pack: malformed command %q", line)
	}
	return receivePackCommand{OldOID: fields[0], NewOID: fields[1], Ref: fields[2]}, nil
}
