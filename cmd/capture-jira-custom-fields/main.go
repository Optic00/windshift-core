package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"windshift/internal/jira"
)

type fixture struct {
	CapturedAt      string                        `json:"captured_at"`
	ProjectKey      string                        `json:"project_key"`
	ProjectID       string                        `json:"project_id"`
	DeploymentType  string                        `json:"deployment_type"`
	FieldCount      int                           `json:"field_count"`
	SuggestionCount int                           `json:"suggestion_count"`
	Fields          []jira.JiraCustomField        `json:"fields"`
	Suggestions     []jira.FieldMappingSuggestion `json:"suggestions"`
}

func loadDotEnv(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path) //nolint:gosec // operator-supplied CLI input path for local capture utility
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
		out[key] = value
	}
	return out, s.Err()
}

func envValue(env map[string]string, key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return env[key]
}

func main() {
	envPath := flag.String("env", ".env", "path to .env with JIRA_INSTANCE_URL, JIRA_EMAIL, JIRA_API_TOKEN, JIRA_PROJECT_KEY")
	outPath := flag.String("out", "", "output JSON path (stdout when empty)")
	flag.Parse()

	env, err := loadDotEnv(*envPath)
	if err != nil {
		panic(err)
	}
	for _, key := range []string{"JIRA_INSTANCE_URL", "JIRA_EMAIL", "JIRA_API_TOKEN", "JIRA_PROJECT_KEY"} {
		if envValue(env, key) == "" {
			panic(fmt.Sprintf("%s is required", key))
		}
	}

	deployment := jira.DeploymentCloud
	if strings.EqualFold(envValue(env, "JIRA_DEPLOYMENT_TYPE"), "datacenter") {
		deployment = jira.DeploymentDataCenter
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client, err := jira.NewClient(jira.Config{
		InstanceURL:    envValue(env, "JIRA_INSTANCE_URL"),
		Email:          envValue(env, "JIRA_EMAIL"),
		APIToken:       envValue(env, "JIRA_API_TOKEN"),
		DeploymentType: deployment,
		Timeout:        45 * time.Second,
	})
	if err != nil {
		panic(err)
	}
	if _, err := client.TestConnection(ctx); err != nil {
		panic(err)
	}

	project, err := client.GetProject(ctx, envValue(env, "JIRA_PROJECT_KEY"))
	if err != nil {
		panic(err)
	}
	fields, err := client.GetProjectFields(ctx, []string{project.ID})
	if err != nil || len(fields) == 0 {
		fields, err = client.ListCustomFields(ctx)
		if err != nil {
			panic(err)
		}
	}

	data := fixture{
		CapturedAt:      time.Now().UTC().Format(time.RFC3339),
		ProjectKey:      project.Key,
		ProjectID:       project.ID,
		DeploymentType:  string(deployment),
		FieldCount:      len(fields),
		SuggestionCount: len(jira.SuggestFieldMappings(fields)),
		Fields:          fields,
		Suggestions:     jira.SuggestFieldMappings(fields),
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		panic(err)
	}
	b = append(b, '\n')
	if *outPath == "" {
		_, _ = os.Stdout.Write(b)
		return
	}
	if err := os.WriteFile(*outPath, b, 0o600); err != nil {
		panic(err)
	}
}
