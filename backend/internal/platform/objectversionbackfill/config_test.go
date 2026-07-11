package objectversionbackfill

import (
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	environment := validEnvironment()
	environment[EnvEndpoint] = "minio.internal:9000"
	environment[EnvSecure] = "true"
	config, err := LoadConfig(mapLookup(environment))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if config.Endpoint != "minio.internal:9000" || !config.Secure || config.Concurrency != 4 {
		t.Fatalf("config = %#v", config)
	}
	if config.AccessKey != environment[EnvAccessKey] || config.SecretKey != environment[EnvSecretKey] {
		t.Fatal("credentials changed while loading configuration")
	}
}

func TestLoadConfigRejectsInvalidInputsWithoutLeakingSecrets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]string)
		want   string
	}{
		{name: "missing endpoint", mutate: func(env map[string]string) { delete(env, EnvEndpoint) }, want: EnvEndpoint},
		{name: "invalid bucket", mutate: func(env map[string]string) { env[EnvBucket] = "Not Valid" }, want: EnvBucket},
		{name: "invalid secure", mutate: func(env map[string]string) { env[EnvSecure] = "sometimes" }, want: EnvSecure},
		{name: "URL endpoint", mutate: func(env map[string]string) { env[EnvEndpoint] = "https://minio:9000" }, want: "host and optional port"},
		{name: "missing region", mutate: func(env map[string]string) { delete(env, EnvRegion) }, want: EnvRegion},
		{name: "zero concurrency", mutate: func(env map[string]string) { env[EnvConcurrency] = "0" }, want: EnvConcurrency},
		{name: "excessive concurrency", mutate: func(env map[string]string) { env[EnvConcurrency] = "33" }, want: EnvConcurrency},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := validEnvironment()
			test.mutate(environment)
			_, err := LoadConfig(mapLookup(environment))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadConfig error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "root-secret-value") {
				t.Fatalf("error leaked secret: %v", err)
			}
		})
	}
}

func validEnvironment() map[string]string {
	return map[string]string{
		EnvEndpoint:    "minio:9000",
		EnvBucket:      "emfont",
		EnvAccessKey:   "root-access-value",
		EnvSecretKey:   "root-secret-value",
		EnvSecure:      "false",
		EnvRegion:      "",
		EnvConcurrency: "4",
	}
}

func mapLookup(environment map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, exists := environment[name]
		return value, exists
	}
}
