package configapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	api "github.com/tsouza/runnerscout/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

func credentialFixture(t *testing.T) (Resolved, *fake.Clientset) {
	t.Helper()
	snapshot := fixture()
	aws := snapshot.Providers["aws"]
	aws.Spec.CredentialEnvironment = map[string]api.SecretKeyReference{"AWS_ACCESS_KEY_ID": {Name: "aws-auth", Key: "access-id"}}
	snapshot.Providers["aws"] = aws
	gcp := snapshot.Providers["gcp"]
	gcp.Spec.CredentialEnvironment = map[string]api.SecretKeyReference{"GOOGLE_APPLICATION_CREDENTIALS": {Name: "gcp-auth", Key: "path"}}
	snapshot.Providers["gcp"] = gcp
	resolved, err := Compile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	secret := func(name string, data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: name, UID: types.UID("uid-" + name), ResourceVersion: "1"}, Data: data}
	}
	return resolved, fake.NewClientset(
		secret("github", map[string][]byte{"token": []byte("fixture-github-token\n"), "unused": []byte("unreferenced-sensitive-key")}),
		secret("aws-auth", map[string][]byte{"access-id": []byte("fixture-aws-key")}),
		secret("gcp-auth", map[string][]byte{"path": []byte("/etc/runnerscout/providers/gcp/key.json")}),
	)
}

func TestResolveSecretsUsesNamedLocalKeysAndRedactsMaterial(t *testing.T) {
	resolved, client := credentialFixture(t)
	credentials, err := ResolveSecrets(context.Background(), client.CoreV1().Secrets("test"), resolved)
	if err != nil {
		t.Fatal(err)
	}
	if string(credentials.GitHub) != "fixture-github-token" || credentials.Providers["aws"]["AWS_ACCESS_KEY_ID"] != "fixture-aws-key" || len(credentials.Providers["aws"]) != 1 || len(credentials.Providers["gcp"]) != 1 {
		t.Fatal("credential mapping differs from references")
	}
	reads := map[string]int{}
	for _, action := range client.Actions() {
		get, ok := action.(kt.GetAction)
		if !ok || action.GetNamespace() != "test" || action.GetResource().Resource != "secrets" {
			t.Fatal("unexpected Secret operation", action)
		}
		reads[get.GetName()]++
	}
	for _, name := range []string{"github", "aws-auth", "gcp-auth"} {
		if reads[name] != 2 {
			t.Fatalf("%s read %d times", name, reads[name])
		}
	}
	if len(reads) != 3 {
		t.Fatal("read unreferenced Secrets")
	}
	encoded, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := string(encoded) + fmt.Sprintf("%v %+v %#v", credentials, credentials, credentials)
	for _, sensitive := range []string{"fixture-github-token", "fixture-aws-key", "unreferenced-sensitive-key", "/etc/runnerscout"} {
		if strings.Contains(diagnostics, sensitive) {
			t.Fatal("credentials escaped through diagnostics")
		}
	}
	credentials.GitHub[0] = 'X'
	original, err := client.CoreV1().Secrets("test").Get(context.Background(), "github", metav1.GetOptions{})
	if err != nil || string(original.Data["token"]) != "fixture-github-token\n" {
		t.Fatal("returned credentials alias Kubernetes state")
	}
}

func TestResolveSecretsRejectsRotationRecreationAndNamespaceSpoofing(t *testing.T) {
	for _, mode := range []string{"rotation", "recreation", "deletion", "namespace", "identity", "recheck-error"} {
		t.Run(mode, func(t *testing.T) {
			resolved, client := credentialFixture(t)
			reads := 0
			client.PrependReactor("get", "secrets", func(action kt.Action) (bool, runtime.Object, error) {
				name := action.(kt.GetAction).GetName()
				if name != "github" {
					return false, nil, nil
				}
				reads++
				obj, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("secrets"), "test", name)
				if err != nil {
					t.Fatal(err)
				}
				object := obj.(*corev1.Secret).DeepCopy()
				if reads == 2 {
					switch mode {
					case "rotation":
						object.ResourceVersion = "2"
						object.Data["token"] = []byte("rotated")
					case "recreation":
						object.UID = "recreated"
					case "deletion":
						now := metav1.Now()
						object.DeletionTimestamp = &now
					case "recheck-error":
						return true, nil, errors.New("secret diagnostic")
					}
				}
				if mode == "namespace" {
					object.Namespace = "foreign"
				}
				if mode == "identity" {
					object.UID = ""
				}
				return true, object, nil
			})
			credentials, err := ResolveSecrets(context.Background(), client.CoreV1().Secrets("test"), resolved)
			if err == nil || len(credentials.GitHub) != 0 || len(credentials.Providers) != 0 || strings.Contains(err.Error(), "secret diagnostic") {
				t.Fatal("inconsistent snapshot returned material", err)
			}
		})
	}
}

func TestResolveSecretsRejectsMissingKeysAndInvalidEnvironment(t *testing.T) {
	for _, mode := range []string{"missing", "empty", "nul", "unsafe-variable", "blank-pat", "missing-object"} {
		t.Run(mode, func(t *testing.T) {
			resolved, client := credentialFixture(t)
			if mode == "unsafe-variable" {
				resolved.Credentials["aws"]["PATH"] = api.SecretKeyReference{Name: "aws-auth", Key: "access-id"}
			}
			if mode == "missing-object" {
				resolved.Auth.SecretRef.Name = "absent"
			}
			client.PrependReactor("get", "secrets", func(action kt.Action) (bool, runtime.Object, error) {
				name := action.(kt.GetAction).GetName()
				if name != "aws-auth" && !(name == "github" && mode == "blank-pat") {
					return false, nil, nil
				}
				obj, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("secrets"), "test", name)
				if err != nil {
					t.Fatal(err)
				}
				object := obj.(*corev1.Secret).DeepCopy()
				switch mode {
				case "missing":
					delete(object.Data, "access-id")
				case "empty":
					object.Data["access-id"] = nil
				case "nul":
					object.Data["access-id"] = []byte("invalid\x00environment")
				case "blank-pat":
					object.Data["token"] = []byte(" \n")
				}
				return true, object, nil
			})
			credentials, err := ResolveSecrets(context.Background(), client.CoreV1().Secrets("test"), resolved)
			if err == nil || len(credentials.GitHub) != 0 || len(credentials.Providers) != 0 {
				t.Fatal("invalid credentials accepted", err)
			}
		})
	}
}
