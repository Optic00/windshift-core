package wscli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

// `ws skill` is the agent-facing read surface of the workspace agent-skills
// library (WI-258): a run's initial prompt indexes the binding's attached
// skills, and the agent fetches a body on demand — `ls` is the index,
// `get` is the disclosure. Authoring happens in the workspace settings UI.

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Read workspace agent skills (knowledge packs)",
	Long: `Read the workspace's agent skills — curated markdown knowledge packs.

A coding-agent run's prompt lists the skills attached to its binding; read a
skill's full body with "ws skill get <id>" when it is relevant to the task.`,
}

var skillListCmd = &cobra.Command{
	Use:     "ls",
	Aliases: []string{"list"},
	Short:   "List the workspace's enabled agent skills",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		skills, err := client.ListAgentSkills(wsID)
		if err != nil {
			return fmt.Errorf("failed to list agent skills: %w", err)
		}
		NewOutput().Print(skills)
		return nil
	},
}

var skillGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get an agent skill's full markdown body",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := NewClient()
		if err != nil {
			return err
		}
		wsID, err := resolveRequiredWorkspace(client)
		if err != nil {
			return err
		}
		skillID, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid skill id: %s", args[0])
		}
		skill, err := client.GetAgentSkill(wsID, skillID)
		if err != nil {
			return fmt.Errorf("failed to get agent skill: %w", err)
		}
		// Markdown body to stdout regardless of -o, mirroring `ws page get`:
		// the agent consumes the content, not the envelope.
		fmt.Printf("# %s\n\n", skill.Name)
		if skill.Description != "" {
			fmt.Printf("_%s_\n\n", skill.Description)
		}
		fmt.Println(skill.Body)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(skillCmd)
	skillCmd.AddCommand(skillListCmd)
	skillCmd.AddCommand(skillGetCmd)
}
