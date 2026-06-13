package redpanda

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"norn/v2/api/model"
)

type Config struct {
	Brokers []string
	RPKPath string
	Timeout time.Duration
}

type Client struct {
	brokers []string
	rpkPath string
	timeout time.Duration
}

type ProvisionResult struct {
	Env    map[string]string
	Topics []ProvisionedTopic
}

type ProvisionedTopic struct {
	Name    string
	Created bool
}

var topicNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var envUnsafeRe = regexp.MustCompile(`[^A-Za-z0-9_]+`)

func NewClient(cfg Config) (*Client, error) {
	brokers := normalizeBrokers(cfg.Brokers)
	if len(brokers) == 0 {
		return nil, fmt.Errorf("redpanda brokers are not configured")
	}
	rpkPath := cfg.RPKPath
	if rpkPath == "" {
		rpkPath = "rpk"
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return &Client{brokers: brokers, rpkPath: rpkPath, timeout: timeout}, nil
}

func (c *Client) Healthy(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("redpanda client is not configured")
	}
	_, err := c.runRPK(ctx, "cluster", "info")
	return err
}

func (c *Client) ProvisionAppKafka(ctx context.Context, _ string, spec *model.KafkaInfra) (*ProvisionResult, error) {
	result := &ProvisionResult{Env: c.env(nil)}
	if spec == nil {
		return result, nil
	}

	topics := normalizeTopics(spec.Topics)
	if len(topics) == 0 {
		return result, nil
	}
	for _, topic := range topics {
		if err := ValidateTopicName(topic); err != nil {
			return nil, err
		}
	}

	args := append([]string{"topic", "create", "--if-not-exists"}, topics...)
	if _, err := c.runRPK(ctx, args...); err != nil {
		return nil, err
	}

	result.Env = c.env(topics)
	for _, topic := range topics {
		result.Topics = append(result.Topics, ProvisionedTopic{Name: topic})
	}
	return result, nil
}

func (c *Client) Brokers() []string {
	if c == nil {
		return nil
	}
	out := append([]string(nil), c.brokers...)
	return out
}

func (c *Client) env(topics []string) map[string]string {
	joined := strings.Join(c.brokers, ",")
	env := map[string]string{
		"KAFKA_BOOTSTRAP_SERVERS": joined,
		"REDPANDA_BROKERS":        joined,
		"RPK_BROKERS":             joined,
	}
	if len(topics) == 0 {
		return env
	}
	env["KAFKA_TOPICS"] = strings.Join(topics, ",")
	for _, topic := range topics {
		alias := strings.ToUpper(envUnsafeRe.ReplaceAllString(topic, "_"))
		alias = strings.Trim(alias, "_")
		if alias != "" {
			env["KAFKA_TOPIC_"+alias] = topic
		}
	}
	return env
}

func (c *Client) runRPK(ctx context.Context, args ...string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("redpanda client is not configured")
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	rpkArgs := append([]string{}, args...)
	rpkArgs = append(rpkArgs, "-X", "brokers="+strings.Join(c.brokers, ","))
	cmd := exec.CommandContext(timeoutCtx, c.rpkPath, rpkArgs...)
	out, err := cmd.CombinedOutput()
	if timeoutCtx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("rpk %s timed out after %s", strings.Join(args, " "), c.timeout)
	}
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" {
			return string(out), fmt.Errorf("rpk %s: %w", strings.Join(args, " "), err)
		}
		return string(out), fmt.Errorf("rpk %s: %w: %s", strings.Join(args, " "), err, trimmed)
	}
	return string(out), nil
}

func ValidateTopicName(topic string) error {
	if topic == "" {
		return fmt.Errorf("kafka topic name is required")
	}
	if len(topic) > 249 {
		return fmt.Errorf("kafka topic %q is longer than 249 characters", topic)
	}
	if topic == "." || topic == ".." {
		return fmt.Errorf("kafka topic %q is reserved", topic)
	}
	if !topicNameRe.MatchString(topic) {
		return fmt.Errorf("kafka topic %q must contain only letters, numbers, dots, underscores, or hyphens", topic)
	}
	return nil
}

func normalizeBrokers(values []string) []string {
	seen := map[string]bool{}
	var brokers []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			brokers = append(brokers, part)
		}
	}
	return brokers
}

func normalizeTopics(values []string) []string {
	seen := map[string]bool{}
	var topics []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		topics = append(topics, value)
	}
	sort.Strings(topics)
	return topics
}
