package awsbrowser

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type registryFactoryFake struct {
	mu      sync.Mutex
	calls   int
	runtime RuntimeContext
	err     error
}

func (factory *registryFactoryFake) Resolve(context.Context, ContextSpec) (RuntimeContext, error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	factory.calls++
	return factory.runtime, factory.err
}

func (factory *registryFactoryFake) callCount() int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.calls
}

type registryRuntimeFake struct {
	mu       sync.Mutex
	identity VerifiedIdentity
}

func (runtime *registryRuntimeFake) Identity() VerifiedIdentity {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.identity
}

func (runtime *registryRuntimeFake) setGeneration(generation uint64) {
	runtime.mu.Lock()
	runtime.identity.CredentialGeneration = generation
	runtime.mu.Unlock()
}

func (*registryRuntimeFake) STS() STSAPI               { return nil }
func (*registryRuntimeFake) EC2() EC2API               { return nil }
func (*registryRuntimeFake) IAM() IAMAPI               { return nil }
func (*registryRuntimeFake) Route53() Route53API       { return nil }
func (*registryRuntimeFake) CloudFront() CloudFrontAPI { return nil }
func (*registryRuntimeFake) ELBV2() ELBV2API           { return nil }
func (*registryRuntimeFake) S3() S3API                 { return nil }

func TestContextRegistryIsLazyAndMemoized(t *testing.T) {
	spec := ContextSpec{Mode: ContextModeNamedProfile, Profile: "dev", Region: "us-east-1"}
	runtime := &registryRuntimeFake{identity: testRegistryIdentity(1)}
	factory := &registryFactoryFake{runtime: runtime}
	registry := NewContextRegistry(factory)

	if view := registry.View(spec); view.Resolved || factory.callCount() != 0 {
		t.Fatalf("view resolved runtime: view=%+v calls=%d", view, factory.callCount())
	}
	first, err := registry.Resolve(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Resolve(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || factory.callCount() != 1 {
		t.Fatalf("runtime was not memoized: calls=%d", factory.callCount())
	}
	if view := registry.View(spec); !view.Resolved || view.Identity.CredentialGeneration != 1 {
		t.Fatalf("unexpected registry view: %+v", view)
	}
}

func TestContextRegistryInvalidatesContextChangesAndGenerationShifts(t *testing.T) {
	spec := ContextSpec{Mode: ContextModeAmbient, Region: "ap-northeast-2"}
	runtime := &registryRuntimeFake{identity: testRegistryIdentity(1)}
	factory := &registryFactoryFake{runtime: runtime}
	registry := NewContextRegistry(factory)
	if _, err := registry.Resolve(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if registry.Invalidate(spec, errors.New("unrelated")) {
		t.Fatal("unrelated error invalidated runtime")
	}
	if !registry.Invalidate(spec, ErrContextChanged) {
		t.Fatal("context change did not invalidate runtime")
	}
	if _, err := registry.Resolve(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	runtime.setGeneration(2)
	if _, err := registry.Resolve(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if factory.callCount() != 3 {
		t.Fatalf("generation shift did not re-resolve: calls=%d", factory.callCount())
	}
	if !registry.InvalidateGeneration(spec, 3) || registry.View(spec).Resolved {
		t.Fatal("explicit generation shift remained memoized")
	}
}

func testRegistryIdentity(generation uint64) VerifiedIdentity {
	return VerifiedIdentity{
		Partition: "aws", AccountID: "123456789012",
		PrincipalARN: "arn:aws:iam::123456789012:user/test", CredentialGeneration: generation,
	}
}
