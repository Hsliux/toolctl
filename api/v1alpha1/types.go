package v1alpha1

import (
	"encoding/json"
	"time"
)

const APIVersion = "toolctl.io/v1alpha1"

type NameMode string

const (
	NameNone     NameMode = "none"
	NameOptional NameMode = "optional"
	NameRequired NameMode = "required"
)

type TargetMode string

const (
	TargetNone     TargetMode = "none"
	TargetOptional TargetMode = "optional"
	TargetRequired TargetMode = "required"
)

type OptionType string

const (
	OptionString      OptionType = "string"
	OptionBool        OptionType = "bool"
	OptionInt         OptionType = "int"
	OptionDuration    OptionType = "duration"
	OptionStringSlice OptionType = "stringSlice"
)

type ColumnType string

const (
	ColumnString          ColumnType = "string"
	ColumnInteger         ColumnType = "integer"
	ColumnBoolean         ColumnType = "boolean"
	ColumnDecimal         ColumnType = "decimal"
	ColumnPercent         ColumnType = "percent"
	ColumnBytes           ColumnType = "bytes"
	ColumnBytesPerSecond  ColumnType = "bytes-per-second"
	ColumnDurationSeconds ColumnType = "duration-seconds"
	ColumnCount           ColumnType = "count"
	ColumnNamedBytesList  ColumnType = "named-bytes-list"
)

type ResultStatus string

const (
	StatusSuccess ResultStatus = "success"
	StatusPartial ResultStatus = "partial"
	StatusFailure ResultStatus = "failure"
)

type ErrorCode string

const (
	ErrorInvalidArgument       ErrorCode = "INVALID_ARGUMENT"
	ErrorConfig                ErrorCode = "CONFIG_ERROR"
	ErrorPluginNotFound        ErrorCode = "PLUGIN_NOT_FOUND"
	ErrorPluginIncompatible    ErrorCode = "PLUGIN_INCOMPATIBLE"
	ErrorPluginProtocol        ErrorCode = "PLUGIN_PROTOCOL_ERROR"
	ErrorUnsupportedPlatform   ErrorCode = "UNSUPPORTED_PLATFORM"
	ErrorUnsupportedCapability ErrorCode = "UNSUPPORTED_CAPABILITY"
	ErrorDependencyMissing     ErrorCode = "DEPENDENCY_MISSING"
	ErrorAuthenticationFailed  ErrorCode = "AUTHENTICATION_FAILED"
	ErrorPermissionDenied      ErrorCode = "PERMISSION_DENIED"
	ErrorTimeout               ErrorCode = "TIMEOUT"
	ErrorCancelled             ErrorCode = "CANCELLED"
	ErrorExecutionFailed       ErrorCode = "EXECUTION_FAILED"
	ErrorOutputLimitExceeded   ErrorCode = "OUTPUT_LIMIT_EXCEEDED"
	ErrorParseFailed           ErrorCode = "PARSE_FAILED"
	ErrorPartialFailure        ErrorCode = "PARTIAL_FAILURE"
	ErrorInternal              ErrorCode = "INTERNAL"
)

type CommandPathSpec struct {
	Path    []string   `json:"path" yaml:"path"`
	Aliases [][]string `json:"aliases" yaml:"aliases"`
}

type OptionSpec struct {
	Name        string          `json:"name" yaml:"name"`
	Shorthand   string          `json:"shorthand,omitempty" yaml:"shorthand,omitempty"`
	Type        OptionType      `json:"type" yaml:"type"`
	Required    bool            `json:"required" yaml:"required"`
	Default     json.RawMessage `json:"default,omitempty" yaml:"-"`
	Description string          `json:"description" yaml:"description"`
}

type ColumnHint struct {
	Header string     `json:"header" yaml:"header"`
	Path   string     `json:"path" yaml:"path"`
	Type   ColumnType `json:"type" yaml:"type"`
	Wide   bool       `json:"wide" yaml:"wide"`
	Order  int        `json:"order" yaml:"order"`
}

type Capability struct {
	ID              string          `json:"id" yaml:"id"`
	Domain          string          `json:"domain" yaml:"domain"`
	Resource        string          `json:"resource" yaml:"resource"`
	Verb            string          `json:"verb" yaml:"verb"`
	Command         CommandPathSpec `json:"command" yaml:"command"`
	Summary         string          `json:"summary" yaml:"summary"`
	NameMode        NameMode        `json:"nameMode" yaml:"nameMode"`
	TargetMode      TargetMode      `json:"targetMode" yaml:"targetMode"`
	Mutating        bool            `json:"mutating" yaml:"mutating"`
	PassthroughArgs bool            `json:"passthroughArgs,omitempty" yaml:"passthroughArgs,omitempty"`
	Options         []OptionSpec    `json:"options" yaml:"options"`
	Columns         []ColumnHint    `json:"columns" yaml:"columns"`
}

type Target struct {
	Name    string            `json:"name" yaml:"name"`
	Address string            `json:"address,omitempty" yaml:"address,omitempty"`
	Labels  map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

type Operation struct {
	APIVersion string                     `json:"apiVersion" yaml:"apiVersion"`
	ID         string                     `json:"id" yaml:"id"`
	Capability string                     `json:"capability" yaml:"capability"`
	Name       string                     `json:"name,omitempty" yaml:"name,omitempty"`
	Arguments  []string                   `json:"arguments,omitempty" yaml:"arguments,omitempty"`
	Targets    []Target                   `json:"targets" yaml:"targets"`
	Options    map[string]json.RawMessage `json:"options" yaml:"-"`
	TimeoutMS  int64                      `json:"timeoutMs" yaml:"timeoutMs"`
}

type Item struct {
	Target string          `json:"target,omitempty" yaml:"target,omitempty"`
	Name   string          `json:"name,omitempty" yaml:"name,omitempty"`
	Kind   string          `json:"kind" yaml:"kind"`
	Data   json.RawMessage `json:"data" yaml:"-"`
}

type Diagnostic struct {
	Code    string            `json:"code" yaml:"code"`
	Message string            `json:"message" yaml:"message"`
	Target  string            `json:"target,omitempty" yaml:"target,omitempty"`
	Details map[string]string `json:"details" yaml:"details"`
}

type TargetError struct {
	Target    string            `json:"target,omitempty" yaml:"target,omitempty"`
	Code      ErrorCode         `json:"code" yaml:"code"`
	Message   string            `json:"message" yaml:"message"`
	Retryable bool              `json:"retryable" yaml:"retryable"`
	Details   map[string]string `json:"details" yaml:"details"`
}

type OperationRef struct {
	ID         string `json:"id" yaml:"id"`
	Capability string `json:"capability" yaml:"capability"`
}

type ModuleInfo struct {
	Name           string `json:"name" yaml:"name"`
	Version        string `json:"version" yaml:"version"`
	MaxConcurrency int    `json:"maxConcurrency,omitempty" yaml:"maxConcurrency,omitempty"`
}

type ModuleRef struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
}

type ResultMetadata struct {
	GeneratedAt time.Time `json:"generatedAt" yaml:"generatedAt"`
	DurationMS  int64     `json:"durationMs" yaml:"durationMs"`
	Module      ModuleRef `json:"module" yaml:"module"`
}

type Result struct {
	APIVersion string         `json:"apiVersion" yaml:"apiVersion"`
	Kind       string         `json:"kind" yaml:"kind"`
	Status     ResultStatus   `json:"status" yaml:"status"`
	Operation  OperationRef   `json:"operation" yaml:"operation"`
	Items      []Item         `json:"items" yaml:"items"`
	Warnings   []Diagnostic   `json:"warnings" yaml:"warnings"`
	Errors     []TargetError  `json:"errors" yaml:"errors"`
	Metadata   ResultMetadata `json:"metadata" yaml:"metadata"`
}

type ExecutionResponse struct {
	APIVersion  string        `json:"apiVersion"`
	Kind        string        `json:"kind"`
	OperationID string        `json:"operationId"`
	Items       []Item        `json:"items"`
	Warnings    []Diagnostic  `json:"warnings"`
	Errors      []TargetError `json:"errors"`
}

type CheckStatus string

const (
	CheckPass CheckStatus = "pass"
	CheckWarn CheckStatus = "warn"
	CheckFail CheckStatus = "fail"
	CheckSkip CheckStatus = "skip"
)

type CheckResult struct {
	Name       string            `json:"name" yaml:"name"`
	Status     CheckStatus       `json:"status" yaml:"status"`
	Message    string            `json:"message" yaml:"message"`
	Suggestion string            `json:"suggestion,omitempty" yaml:"suggestion,omitempty"`
	Details    map[string]string `json:"details" yaml:"details"`
}
