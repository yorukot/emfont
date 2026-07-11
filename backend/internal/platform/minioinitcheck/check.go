package minioinitcheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type userInfo struct {
	AccessKey  string        `json:"accessKey"`
	Status     string        `json:"status"`
	UserStatus string        `json:"userStatus"`
	PolicyName string        `json:"policyName"`
	MemberOf   []groupMember `json:"memberOf"`
}

type groupMember struct {
	Name string `json:"name"`
}

type policyInfo struct {
	Status     string         `json:"status"`
	Policy     string         `json:"policy"`
	PolicyInfo policyDocument `json:"policyInfo"`
	IsGroup    *bool          `json:"isGroup"`
}

type policyDocument struct {
	PolicyName string     `json:"PolicyName"`
	Policy     policyBody `json:"Policy"`
	CreateDate string     `json:"CreateDate"`
	UpdateDate string     `json:"UpdateDate"`
}

type policyBody struct {
	Version   string            `json:"Version"`
	Statement []policyStatement `json:"Statement"`
}

type policyStatement struct {
	Effect    string           `json:"Effect"`
	Action    []string         `json:"Action"`
	Resource  []string         `json:"Resource"`
	Condition *policyCondition `json:"Condition,omitempty"`
}

type policyCondition struct {
	StringLike map[string][]string `json:"StringLike"`
}

type identityProviderInfo struct {
	Status string                   `json:"status"`
	Config []identityProviderTarget `json:"config"`
}

type identityProviderTarget struct {
	SubSystem string               `json:"subSystem"`
	Target    string               `json:"target,omitempty"`
	KV        []identityProviderKV `json:"kv"`
}

type identityProviderKV struct {
	Key         string                       `json:"key"`
	Value       string                       `json:"value"`
	EnvOverride *identityProviderEnvOverride `json:"envOverride,omitempty"`
}

type identityProviderEnvOverride struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type anonymousInfo struct {
	Operation  string          `json:"operation"`
	Status     string          `json:"status"`
	Bucket     string          `json:"bucket"`
	Permission string          `json:"permission"`
	Anonymous  json.RawMessage `json:"anonymous,omitempty"`
}

type lifecycleInfo struct {
	Status    string          `json:"status"`
	Target    string          `json:"target"`
	Config    lifecycleConfig `json:"config"`
	UpdatedAt string          `json:"updatedAt"`
}

type lifecycleConfig struct {
	Rules []lifecycleRule `json:"Rules"`
}

type lifecycleRule struct {
	Expiration                  lifecycleExpiration         `json:"Expiration"`
	ID                          string                      `json:"ID"`
	Filter                      lifecycleFilter             `json:"Filter"`
	NoncurrentVersionExpiration noncurrentVersionExpiration `json:"NoncurrentVersionExpiration"`
	Status                      string                      `json:"Status"`
}

type lifecycleExpiration struct {
	ExpiredObjectDeleteMarker bool `json:"ExpiredObjectDeleteMarker"`
}

type lifecycleFilter struct {
	Prefix string `json:"Prefix"`
}

type noncurrentVersionExpiration struct {
	NoncurrentDays int `json:"NoncurrentDays"`
}

// User verifies the complete policy and group state returned by
// `mc admin user info --json`.
func User(reader io.Reader, expectedAccessKey, expectedPolicy string) error {
	if reader == nil {
		return errors.New("user information is required")
	}
	if strings.TrimSpace(expectedPolicy) == "" {
		return errors.New("expected policy is required")
	}
	if expectedAccessKey == "" {
		return errors.New("expected application access key is required")
	}
	var info userInfo
	if err := decodeSingle(reader, &info); err != nil {
		return fmt.Errorf("decode user information: %w", err)
	}
	if info.Status != "success" || info.UserStatus != "enabled" {
		return errors.New("application user is not enabled")
	}
	if info.AccessKey != expectedAccessKey {
		return errors.New("application user identity does not match the expected access key")
	}
	if info.PolicyName != expectedPolicy {
		return errors.New("application user policy set is not exactly the expected policy")
	}
	if len(info.MemberOf) != 0 {
		return errors.New("application user must not belong to any group")
	}
	return nil
}

// Policy verifies the complete application policy returned by
// `mc admin policy info --json`.
func Policy(reader io.Reader, expectedPolicy, expectedBucket string) error {
	expected := []policyStatement{
		{Effect: "Allow", Resource: []string{"arn:aws:s3:::" + expectedBucket}, Action: []string{
			"s3:GetBucketLocation",
			"s3:GetBucketVersioning",
			"s3:ListBucket",
		}},
		{Effect: "Allow", Resource: []string{"arn:aws:s3:::" + expectedBucket + "/*"}, Action: []string{
			"s3:GetObject",
		}},
		{Effect: "Allow", Resource: []string{"arn:aws:s3:::" + expectedBucket + "/_generated/*"}, Action: []string{
			"s3:PutObject",
		}},
	}
	return verifyPolicy(reader, expectedPolicy, expectedBucket, expected)
}

// CleanupPolicy verifies the exact read/delete policy used by fontcleanup.
func CleanupPolicy(reader io.Reader, expectedPolicy, expectedBucket string) error {
	expected := []policyStatement{
		{Effect: "Allow", Resource: []string{"arn:aws:s3:::" + expectedBucket}, Action: []string{
			"s3:GetBucketLocation",
		}},
		{
			Effect: "Allow", Resource: []string{"arn:aws:s3:::" + expectedBucket},
			Action: []string{
				"s3:ListBucket",
			},
			Condition: &policyCondition{StringLike: map[string][]string{
				"s3:prefix": {"_generated/*"},
			}},
		},
		{Effect: "Allow", Resource: []string{"arn:aws:s3:::" + expectedBucket + "/_generated/*"}, Action: []string{
			"s3:DeleteObject",
			"s3:DeleteObjectVersion",
			"s3:GetObject",
		}},
	}
	return verifyPolicy(reader, expectedPolicy, expectedBucket, expected)
}

func verifyPolicy(
	reader io.Reader,
	expectedPolicy, expectedBucket string,
	expected []policyStatement,
) error {
	if reader == nil {
		return errors.New("policy information is required")
	}
	if strings.TrimSpace(expectedPolicy) == "" || strings.TrimSpace(expectedBucket) == "" {
		return errors.New("expected policy and bucket are required")
	}
	var info policyInfo
	if err := decodeSingle(reader, &info); err != nil {
		return fmt.Errorf("decode policy information: %w", err)
	}
	if info.Status != "success" || info.Policy != expectedPolicy ||
		info.PolicyInfo.PolicyName != expectedPolicy {
		return errors.New("application policy identity does not match the expected policy")
	}
	if info.IsGroup == nil || *info.IsGroup {
		return errors.New("application policy must be a direct user policy")
	}
	if _, err := time.Parse(time.RFC3339, info.PolicyInfo.CreateDate); err != nil {
		return errors.New("application policy has an invalid creation timestamp")
	}
	if _, err := time.Parse(time.RFC3339, info.PolicyInfo.UpdateDate); err != nil {
		return errors.New("application policy has an invalid update timestamp")
	}
	if info.PolicyInfo.Policy.Version != "2012-10-17" {
		return errors.New("application policy has an unexpected version")
	}

	if len(info.PolicyInfo.Policy.Statement) != len(expected) {
		return errors.New("application policy has an unexpected statement count")
	}
	matched := make([]bool, len(expected))
	for _, statement := range info.PolicyInfo.Policy.Statement {
		if statement.Effect != "Allow" || len(statement.Resource) != 1 {
			return errors.New("application policy contains an unexpected statement")
		}
		found := false
		for index, expectation := range expected {
			if matched[index] || !policyStatementEqual(statement, expectation) {
				continue
			}
			matched[index] = true
			found = true
			break
		}
		if !found {
			return errors.New("application policy contains an unexpected statement")
		}
	}
	return nil
}

func policyStatementEqual(actual, expected policyStatement) bool {
	return actual.Effect == expected.Effect &&
		equalStringSet(actual.Action, expected.Action) &&
		equalStringSet(actual.Resource, expected.Resource) &&
		policyConditionEqual(actual.Condition, expected.Condition)
}

func policyConditionEqual(actual, expected *policyCondition) bool {
	if actual == nil || expected == nil {
		return actual == nil && expected == nil
	}
	if len(actual.StringLike) != len(expected.StringLike) {
		return false
	}
	for key, expectedValues := range expected.StringLike {
		if !equalStringSet(actual.StringLike[key], expectedValues) {
			return false
		}
	}
	return true
}

// IdentityProviders verifies every configured target using MinIO's activation
// rules. An empty enable value is accepted only when all implicit activation
// fields are effectively empty, including environment overrides.
func IdentityProviders(reader io.Reader, expectedSubsystem string) error {
	if reader == nil {
		return errors.New("identity-provider configuration is required")
	}
	if expectedSubsystem != "identity_openid" && expectedSubsystem != "identity_ldap" {
		return errors.New("expected identity-provider subsystem is invalid")
	}
	var info identityProviderInfo
	if err := decodeSingle(reader, &info); err != nil {
		return fmt.Errorf("decode identity-provider configuration: %w", err)
	}
	if info.Status != "success" || len(info.Config) == 0 {
		return errors.New("identity-provider configuration query did not return any targets")
	}
	seenTargets := make(map[string]struct{}, len(info.Config))
	for _, target := range info.Config {
		if target.SubSystem != expectedSubsystem {
			return errors.New("identity-provider query returned an unexpected subsystem")
		}
		if _, duplicate := seenTargets[target.Target]; duplicate {
			return errors.New("identity-provider query returned a duplicate target")
		}
		seenTargets[target.Target] = struct{}{}
		seenKeys := make(map[string]struct{}, len(target.KV))
		effective := make(map[string]string, len(target.KV))
		enable := ""
		foundEnable := false
		for _, kv := range target.KV {
			if strings.TrimSpace(kv.Key) == "" {
				return errors.New("identity-provider target contains an empty configuration key")
			}
			if _, duplicate := seenKeys[kv.Key]; duplicate {
				return errors.New("identity-provider target contains a duplicate configuration key")
			}
			seenKeys[kv.Key] = struct{}{}
			value := kv.Value
			if kv.EnvOverride != nil {
				if strings.TrimSpace(kv.EnvOverride.Name) == "" {
					return errors.New("identity-provider environment override has no variable name")
				}
				value = kv.EnvOverride.Value
			}
			effective[kv.Key] = strings.TrimSpace(value)
			if kv.Key == "enable" {
				foundEnable = true
				enable = effective[kv.Key]
			}
		}
		if !foundEnable {
			return errors.New("identity-provider target has no enable state")
		}
		switch strings.ToLower(enable) {
		case "off":
			continue
		case "":
			implicitlyEnabled := effective["server_addr"] != ""
			if expectedSubsystem == "identity_openid" {
				implicitlyEnabled = effective["config_url"] != "" ||
					effective["client_id"] != "" || effective["client_secret"] != ""
			}
			if !implicitlyEnabled {
				continue
			}
		}
		return errors.New("identity-provider target is enabled")
	}
	return nil
}

// Anonymous verifies that MinIO reports no bucket policy after reconciliation.
func Anonymous(reader io.Reader, expectedTarget string) error {
	if reader == nil {
		return errors.New("anonymous-policy information is required")
	}
	if strings.TrimSpace(expectedTarget) == "" {
		return errors.New("expected anonymous-policy target is required")
	}
	var info anonymousInfo
	if err := decodeSingle(reader, &info); err != nil {
		return fmt.Errorf("decode anonymous-policy information: %w", err)
	}
	if info.Status != "success" || info.Operation != "get" || info.Bucket != expectedTarget {
		return errors.New("anonymous-policy query identity does not match the expected bucket")
	}
	if info.Permission != "private" {
		return errors.New("bucket anonymous policy is not private")
	}
	if payload := strings.TrimSpace(string(info.Anonymous)); payload != "" && payload != "null" {
		return errors.New("bucket still has an anonymous policy document")
	}
	return nil
}

// Lifecycle verifies the complete rule set returned by `mc ilm rule ls
// --json`. Unknown fields are rejected so a newly introduced lifecycle action
// cannot silently broaden retention behavior.
func Lifecycle(reader io.Reader, expectedTarget, expectedPrefix string, expectedNoncurrentDays int) error {
	if reader == nil {
		return errors.New("lifecycle information is required")
	}
	if expectedTarget == "" {
		return errors.New("expected lifecycle target is required")
	}
	if expectedPrefix == "" {
		return errors.New("expected lifecycle prefix is required")
	}
	if expectedNoncurrentDays <= 0 {
		return errors.New("expected noncurrent expiry must be positive")
	}
	var info lifecycleInfo
	if err := decodeSingle(reader, &info); err != nil {
		return fmt.Errorf("decode lifecycle information: %w", err)
	}
	if info.Status != "success" {
		return errors.New("lifecycle query did not succeed")
	}
	if info.Target != expectedTarget {
		return errors.New("lifecycle query returned an unexpected target")
	}
	if _, err := time.Parse(time.RFC3339, info.UpdatedAt); err != nil {
		return errors.New("lifecycle query has an invalid update timestamp")
	}
	if len(info.Config.Rules) != 1 {
		return errors.New("bucket lifecycle must contain exactly one rule")
	}
	rule := info.Config.Rules[0]
	if strings.TrimSpace(rule.ID) == "" || rule.Status != "Enabled" {
		return errors.New("bucket lifecycle rule is not enabled")
	}
	if rule.Filter.Prefix != expectedPrefix {
		return errors.New("bucket lifecycle rule has an unexpected prefix")
	}
	if !rule.Expiration.ExpiredObjectDeleteMarker {
		return errors.New("bucket lifecycle rule must expire delete markers")
	}
	if rule.NoncurrentVersionExpiration.NoncurrentDays != expectedNoncurrentDays {
		return errors.New("bucket lifecycle rule has an unexpected noncurrent expiry")
	}
	return nil
}

func decodeSingle(reader io.Reader, destination any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON documents are not allowed")
		}
		return fmt.Errorf("read trailing JSON: %w", err)
	}
	return nil
}

func equalStringSet(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	values := make(map[string]struct{}, len(actual))
	for _, value := range actual {
		if _, duplicate := values[value]; duplicate {
			return false
		}
		values[value] = struct{}{}
	}
	for _, value := range expected {
		if _, ok := values[value]; !ok {
			return false
		}
	}
	return true
}
