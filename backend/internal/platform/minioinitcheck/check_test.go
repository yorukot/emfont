package minioinitcheck

import (
	"strings"
	"testing"
)

func TestUser(t *testing.T) {
	valid := `{"status":"success","accessKey":"app-key","userStatus":"enabled","policyName":"emfont-controller"}`
	if err := User(strings.NewReader(valid), "app-key", "emfont-controller"); err != nil {
		t.Fatalf("User(valid) error = %v", err)
	}

	for _, test := range []struct {
		name string
		json string
	}{
		{name: "wrong access key", json: strings.Replace(valid, `"app-key"`, `"other-key"`, 1)},
		{name: "additional policy", json: `{"status":"success","accessKey":"app-key","userStatus":"enabled","policyName":"consoleAdmin,emfont-controller"}`},
		{name: "group membership", json: `{"status":"success","accessKey":"app-key","userStatus":"enabled","policyName":"emfont-controller","memberOf":[{"name":"admins"}]}`},
		{name: "disabled", json: `{"status":"success","accessKey":"app-key","userStatus":"disabled","policyName":"emfont-controller"}`},
		{name: "unknown field", json: `{"status":"success","accessKey":"app-key","userStatus":"enabled","policyName":"emfont-controller","futurePrivilege":true}`},
		{name: "trailing document", json: valid + ` {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := User(strings.NewReader(test.json), "app-key", "emfont-controller"); err == nil {
				t.Fatal("User returned nil error")
			}
		})
	}
	implicitLDAP := `{"status":"success","config":[{"subSystem":"identity_ldap","kv":[{"key":"enable","value":""},{"key":"server_addr","value":"ldap.internal:636"}]}]}`
	if err := IdentityProviders(strings.NewReader(implicitLDAP), "identity_ldap"); err == nil {
		t.Fatal("IdentityProviders accepted implicitly enabled LDAP")
	}
}

func TestPolicy(t *testing.T) {
	valid := `{
  "status":"success",
  "policy":"emfont-controller",
  "policyInfo":{
    "PolicyName":"emfont-controller",
    "Policy":{"Version":"2012-10-17","Statement":[
	      {"Effect":"Allow","Action":["s3:GetBucketLocation","s3:GetBucketVersioning","s3:ListBucket"],"Resource":["arn:aws:s3:::emfont"]},
	      {"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::emfont/*"]},
	      {"Effect":"Allow","Action":["s3:PutObject"],"Resource":["arn:aws:s3:::emfont/_generated/*"]}
    ]},
    "CreateDate":"2026-07-11T01:37:17.576Z",
    "UpdateDate":"2026-07-11T01:37:17.576Z"
  },
  "isGroup":false
}`
	if err := Policy(strings.NewReader(valid), "emfont-controller", "emfont"); err != nil {
		t.Fatalf("Policy(valid) error = %v", err)
	}

	for _, test := range []struct {
		name string
		json string
	}{
		{name: "extra action", json: strings.Replace(valid, `"s3:GetObject"`, `"s3:GetObject","s3:PutObject"`, 1)},
		{name: "unneeded version action", json: strings.Replace(valid, `"s3:GetObject"`, `"s3:GetObject","s3:GetObjectVersion"`, 1)},
		{name: "wrong bucket", json: strings.Replace(valid, `arn:aws:s3:::emfont/*`, `arn:aws:s3:::other/*`, 1)},
		{name: "group policy", json: strings.Replace(valid, `"isGroup":false`, `"isGroup":true`, 1)},
		{name: "extra condition", json: strings.Replace(valid, `"Effect":"Allow"`, `"Effect":"Allow","Condition":{"Bool":true}`, 1)},
		{name: "extra statement", json: strings.Replace(valid, `]}`, `,{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::other/*"]}]}`, 1)},
		{name: "invalid update timestamp", json: strings.Replace(valid, `"UpdateDate":"2026-07-11T01:37:17.576Z"`, `"UpdateDate":"invalid"`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := Policy(strings.NewReader(test.json), "emfont-controller", "emfont"); err == nil {
				t.Fatal("Policy returned nil error")
			}
		})
	}
}

func TestCleanupPolicy(t *testing.T) {
	valid := `{
  "status":"success",
  "policy":"emfont-cleanup",
	  "policyInfo":{
	    "PolicyName":"emfont-cleanup",
	    "Policy":{"Version":"2012-10-17","Statement":[
	      {"Effect":"Allow","Action":["s3:GetBucketLocation"],"Resource":["arn:aws:s3:::emfont"]},
	      {"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::emfont"],"Condition":{"StringLike":{"s3:prefix":["_generated/*"]}}},
	      {"Effect":"Allow","Action":["s3:DeleteObject","s3:DeleteObjectVersion","s3:GetObject"],"Resource":["arn:aws:s3:::emfont/_generated/*"]}
    ]},
    "CreateDate":"2026-07-11T01:37:17.576Z",
    "UpdateDate":"2026-07-11T01:37:17.576Z"
  },
  "isGroup":false
}`
	if err := CleanupPolicy(strings.NewReader(valid), "emfont-cleanup", "emfont"); err != nil {
		t.Fatalf("CleanupPolicy(valid) error = %v", err)
	}
	for _, action := range []string{"s3:PutObject", "s3:ListBucketMultipartUploads", "s3:AbortMultipartUpload"} {
		modified := strings.Replace(valid, `"s3:GetObject"`, `"s3:GetObject","`+action+`"`, 1)
		if err := CleanupPolicy(strings.NewReader(modified), "emfont-cleanup", "emfont"); err == nil {
			t.Fatalf("CleanupPolicy accepted extra action %q", action)
		}
	}
	for name, modified := range map[string]string{
		"missing prefix condition": strings.Replace(valid, `,"Condition":{"StringLike":{"s3:prefix":["_generated/*"]}}`, "", 1),
		"broad prefix":             strings.Replace(valid, `"_generated/*"`, `"*"`, 1),
		"additional prefix":        strings.Replace(valid, `"_generated/*"`, `"_generated/*","original-fonts/*"`, 1),
		"wrong operator":           strings.Replace(valid, `"StringLike"`, `"StringEquals"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := CleanupPolicy(strings.NewReader(modified), "emfont-cleanup", "emfont"); err == nil {
				t.Fatal("CleanupPolicy accepted an incorrect list-prefix condition")
			}
		})
	}
}

func TestIdentityProviders(t *testing.T) {
	validOIDC := `{"status":"success","config":[
  {"subSystem":"identity_openid","kv":[{"key":"enable","value":"off"},{"key":"config_url","value":""}]},
  {"subSystem":"identity_openid","target":"secondary","kv":[{"key":"enable","value":"on","envOverride":{"name":"MINIO_IDENTITY_OPENID_ENABLE_SECONDARY","value":"off"}},{"key":"config_url","value":"https://issuer.invalid"}]}
]}`
	if err := IdentityProviders(strings.NewReader(validOIDC), "identity_openid"); err != nil {
		t.Fatalf("IdentityProviders(valid OIDC) error = %v", err)
	}
	validLDAP := `{"status":"success","config":[{"subSystem":"identity_ldap","kv":[{"key":"enable","value":"off"},{"key":"server_addr","value":""}]}]}`
	if err := IdentityProviders(strings.NewReader(validLDAP), "identity_ldap"); err != nil {
		t.Fatalf("IdentityProviders(valid LDAP) error = %v", err)
	}
	inertDefaults := []struct {
		subsystem string
		payload   string
	}{
		{subsystem: "identity_openid", payload: `{"status":"success","config":[{"subSystem":"identity_openid","kv":[{"key":"enable","value":""},{"key":"config_url","value":""},{"key":"client_id","value":""},{"key":"client_secret","value":""},{"key":"claim_name","value":"policy"}]}]}`},
		{subsystem: "identity_ldap", payload: `{"status":"success","config":[{"subSystem":"identity_ldap","kv":[{"key":"enable","value":""},{"key":"server_addr","value":""},{"key":"server_insecure","value":"off"}]}]}`},
	}
	for _, test := range inertDefaults {
		if err := IdentityProviders(strings.NewReader(test.payload), test.subsystem); err != nil {
			t.Fatalf("IdentityProviders(inert %s) error = %v", test.subsystem, err)
		}
	}

	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "implicit OIDC", payload: `{"status":"success","config":[{"subSystem":"identity_openid","kv":[{"key":"enable","value":""},{"key":"config_url","value":"https://issuer.invalid"}]}]}`},
		{name: "implicit OIDC env", payload: `{"status":"success","config":[{"subSystem":"identity_openid","kv":[{"key":"enable","value":""},{"key":"config_url","value":"","envOverride":{"name":"MINIO_IDENTITY_OPENID_CONFIG_URL","value":"https://issuer.invalid"}},{"key":"client_id","value":""},{"key":"client_secret","value":""}]}]}`},
		{name: "enabled later target", payload: strings.Replace(validOIDC, `"target":"secondary","kv":[{"key":"enable","value":"on","envOverride":{"name":"MINIO_IDENTITY_OPENID_ENABLE_SECONDARY","value":"off"}`, `"target":"secondary","kv":[{"key":"enable","value":"on"`, 1)},
		{name: "enabled env override", payload: strings.Replace(validOIDC, `"name":"MINIO_IDENTITY_OPENID_ENABLE_SECONDARY","value":"off"`, `"name":"MINIO_IDENTITY_OPENID_ENABLE_SECONDARY","value":"on"`, 1)},
		{name: "missing enable", payload: `{"status":"success","config":[{"subSystem":"identity_openid","kv":[{"key":"config_url","value":""}]}]}`},
		{name: "duplicate target", payload: strings.Replace(validOIDC, `"target":"secondary",`, "", 1)},
		{name: "unknown field", payload: strings.Replace(validOIDC, `"status":"success"`, `"status":"success","future":true`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := IdentityProviders(strings.NewReader(test.payload), "identity_openid"); err == nil {
				t.Fatal("IdentityProviders returned nil error")
			}
		})
	}
}

func TestAnonymous(t *testing.T) {
	valid := `{"operation":"get","status":"success","bucket":"emfont-bootstrap/emfont","permission":"private"}`
	if err := Anonymous(strings.NewReader(valid), "emfont-bootstrap/emfont"); err != nil {
		t.Fatalf("Anonymous(valid) error = %v", err)
	}
	for _, payload := range []string{
		strings.Replace(valid, `"private"`, `"custom"`, 1),
		strings.Replace(valid, `}`, `,"anonymous":{"Statement":[]}}`, 1),
		strings.Replace(valid, `emfont-bootstrap/emfont`, `other/emfont`, 1),
		strings.Replace(valid, `}`, `,"future":true}`, 1),
	} {
		if err := Anonymous(strings.NewReader(payload), "emfont-bootstrap/emfont"); err == nil {
			t.Fatalf("Anonymous accepted %s", payload)
		}
	}
}

func TestLifecycle(t *testing.T) {
	valid := `{
  "status":"success",
  "target":"emfont-bootstrap/emfont",
  "config":{"Rules":[{
    "Expiration":{"ExpiredObjectDeleteMarker":true},
    "ID":"generated-expiry",
    "Filter":{"Prefix":"_generated/"},
    "NoncurrentVersionExpiration":{"NoncurrentDays":7},
    "Status":"Enabled"
  }]},
  "updatedAt":"2026-07-11T01:04:38Z"
}`
	if err := Lifecycle(strings.NewReader(valid), "emfont-bootstrap/emfont", "_generated/", 7); err != nil {
		t.Fatalf("Lifecycle(valid) error = %v", err)
	}

	for _, test := range []struct {
		name string
		json string
	}{
		{name: "additional rule", json: strings.Replace(valid, `}]}`, `},{"Expiration":{"ExpiredObjectDeleteMarker":true},"ID":"other","Filter":{"Prefix":""},"NoncurrentVersionExpiration":{"NoncurrentDays":1},"Status":"Enabled"}]}`, 1)},
		{name: "wrong target", json: strings.Replace(valid, `emfont-bootstrap/emfont`, `other/bucket`, 1)},
		{name: "wrong prefix", json: strings.Replace(valid, `"_generated/"`, `""`, 1)},
		{name: "wrong expiry", json: strings.Replace(valid, `"NoncurrentDays":7`, `"NoncurrentDays":8`, 1)},
		{name: "delete marker retained", json: strings.Replace(valid, `"ExpiredObjectDeleteMarker":true`, `"ExpiredObjectDeleteMarker":false`, 1)},
		{name: "extra transition", json: strings.Replace(valid, `"Status":"Enabled"`, `"Transition":{"Days":1},"Status":"Enabled"`, 1)},
		{name: "invalid timestamp", json: strings.Replace(valid, `2026-07-11T01:04:38Z`, `not-a-time`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := Lifecycle(strings.NewReader(test.json), "emfont-bootstrap/emfont", "_generated/", 7); err == nil {
				t.Fatal("Lifecycle returned nil error")
			}
		})
	}
}
