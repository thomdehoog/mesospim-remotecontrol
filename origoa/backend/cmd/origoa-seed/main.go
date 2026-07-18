// Command origoa-seed populates a repository with a demonstration
// dataset: schemas, workflow definitions, folders, entries (including
// overlays), documents with entry references, links and comments. It
// exercises the same repository services as the API server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/go-git/go-git/v5/plumbing"

	"origoa/internal/config"
	"origoa/internal/gitstore"
	"origoa/internal/projection"
	"origoa/internal/repo"
	"origoa/internal/scanner"
)

func main() {
	cfgPath := flag.String("config", "", "path to origoa.json")
	flag.Parse()
	ctx := context.Background()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	git, err := gitstore.Open(cfg.GitDir)
	if err != nil {
		log.Fatal(err)
	}
	db, err := projection.Connect(ctx, cfg.Database)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	sc, err := scanner.New(cfg.Scanner, git, db, &scanner.FoundationIndexer{DB: db})
	if err != nil {
		log.Fatal(err)
	}
	svc := repo.New(git, db, sc)
	svc.AuthorName = cfg.Author.Name
	svc.AuthorEmail = cfg.Author.Email
	if err := svc.SyncOrReindex(ctx); err != nil {
		log.Fatal(err)
	}

	head, err := git.Head()
	if err != nil {
		log.Fatal(err)
	}
	if !head.IsZero() {
		log.Fatal("refusing to seed a non-empty repository")
	}

	must := func(err error) {
		if err != nil {
			log.Fatal(err)
		}
	}
	mustGUID := func(guid string, err error) string {
		if err != nil {
			log.Fatal(err)
		}
		return guid
	}

	// Repository configuration: schemas and workflow definitions.
	_, err = svc.Update(ctx, func(h plumbing.Hash, cs *gitstore.Changeset) (string, error) {
		cs.Write(".origoa/schemas/requirement.json", []byte(`{
  "artifactType": "requirement",
  "kind": "entry",
  "displayName": "Requirement",
  "hidPrefix": "REQ",
  "fields": [
    {"id": "description", "displayName": "Description", "type": "richtext"},
    {"id": "priority", "displayName": "Priority", "type": "enum", "options": ["low", "medium", "high"], "indexed": true},
    {"id": "verified", "displayName": "Verified", "type": "boolean"},
    {"id": "due", "displayName": "Due date", "type": "date"}
  ],
  "workflows": ["review"],
  "relationships": [
    {"linkType": "satisfies", "displayName": "Satisfied by", "sourceTypes": ["requirement"], "targetTypes": ["testcase"], "cardinality": "many-to-many"},
    {"linkType": "parent", "displayName": "Parent requirement", "sourceTypes": ["requirement"], "targetTypes": ["requirement"], "cardinality": "many-to-one"}
  ],
  "presentation": {"icon": "requirement", "color": "#2563eb"}
}
`))
		cs.Write(".origoa/schemas/testcase.json", []byte(`{
  "artifactType": "testcase",
  "kind": "entry",
  "displayName": "Test Case",
  "hidPrefix": "TC",
  "fields": [
    {"id": "steps", "displayName": "Steps", "type": "multitext"},
    {"id": "automated", "displayName": "Automated", "type": "boolean", "indexed": true}
  ],
  "workflows": ["review"]
}
`))
		cs.Write(".origoa/schemas/product.json", []byte(`{
  "artifactType": "product",
  "kind": "entry",
  "displayName": "Product",
  "hidPrefix": "PROD",
  "fields": [
    {"id": "sku", "displayName": "SKU", "type": "text", "indexed": true},
    {"id": "voltage", "displayName": "Supply voltage", "type": "text"},
    {"id": "housing", "displayName": "Housing", "type": "enum", "options": ["standard", "rugged", "compact"]},
    {"id": "datasheet", "displayName": "Datasheet", "type": "hyperlink"}
  ]
}
`))
		cs.Write(".origoa/schemas/spec.json", []byte(`{
  "artifactType": "spec",
  "kind": "document",
  "displayName": "Specification",
  "hidPrefix": "SPEC",
  "workflows": ["review"]
}
`))
		cs.Write(".origoa/workflows/review.json", []byte(`{
  "name": "review",
  "displayName": "Review",
  "initial": "draft",
  "states": [
    {"id": "draft", "displayName": "Draft"},
    {"id": "in_review", "displayName": "In Review"},
    {"id": "approved", "displayName": "Approved"},
    {"id": "obsolete", "displayName": "Obsolete"}
  ],
  "transitions": [
    {"name": "Submit", "from": "draft", "to": "in_review"},
    {"name": "Approve", "from": "in_review", "to": "approved"},
    {"name": "Reject", "from": "in_review", "to": "draft"},
    {"name": "Retire", "from": "approved", "to": "obsolete"}
  ]
}
`))
		// A more specialized schema deeper in the hierarchy: safety
		// requirements extend the root definition (lexical inheritance).
		cs.Write("engineering/safety/.origoa/schemas/requirement.json", []byte(`{
  "artifactType": "requirement",
  "fields": [
    {"id": "sil", "displayName": "Safety integrity level", "type": "enum", "options": ["SIL-1", "SIL-2", "SIL-3"], "indexed": true}
  ]
}
`))
		return "Repository configuration created", nil
	})
	must(err)

	// Entries.
	req1 := mustGUID(svc.CreateArtifact(ctx, repo.CreateArtifactParams{
		Kind: "entry", Folder: "engineering/requirements", Type: "requirement",
		Title: "Controller boots within 3 seconds",
		Fields: map[string]any{
			"description": "After power-on the controller <b>must</b> reach operational state within 3 seconds.",
			"priority":    "high", "verified": false, "due": "2026-09-30",
		}}))
	req2 := mustGUID(svc.CreateArtifact(ctx, repo.CreateArtifactParams{
		Kind: "entry", Folder: "engineering/requirements", Type: "requirement",
		Title: "Firmware updates are cryptographically signed",
		Fields: map[string]any{
			"description": "Only firmware images signed by the release key may be accepted.",
			"priority":    "high", "verified": true,
		}}))
	req3 := mustGUID(svc.CreateArtifact(ctx, repo.CreateArtifactParams{
		Kind: "entry", Folder: "engineering/safety", Type: "requirement",
		Title: "Emergency stop cuts power within 100 ms",
		Fields: map[string]any{
			"description": "Pressing the emergency stop must remove actuator power within 100 milliseconds.",
			"priority":    "high", "sil": "SIL-3",
		}}))
	tc1 := mustGUID(svc.CreateArtifact(ctx, repo.CreateArtifactParams{
		Kind: "entry", Folder: "engineering/tests", Type: "testcase",
		Title: "Measure cold-boot time",
		Fields: map[string]any{
			"steps":     "1. Power off for 5 minutes\n2. Apply power\n3. Measure time to READY signal",
			"automated": true,
		}}))
	tc2 := mustGUID(svc.CreateArtifact(ctx, repo.CreateArtifactParams{
		Kind: "entry", Folder: "engineering/tests", Type: "testcase",
		Title: "Reject unsigned firmware image",
		Fields: map[string]any{
			"steps":     "1. Build unsigned image\n2. Attempt update\n3. Expect rejection and audit log entry",
			"automated": false,
		}}))

	// Products with an overlay variant.
	prodBase := mustGUID(svc.CreateArtifact(ctx, repo.CreateArtifactParams{
		Kind: "entry", Folder: "catalog", Type: "product",
		Title: "IO-Controller C100",
		Fields: map[string]any{
			"sku": "C100", "voltage": "24 V DC", "housing": "standard",
			"datasheet": "https://example.com/c100.pdf",
		}}))
	prodVariant := mustGUID(svc.CreateArtifact(ctx, repo.CreateArtifactParams{
		Kind: "entry", Folder: "catalog", Type: "product",
		Title: "IO-Controller C100-R (rugged variant)", Base: prodBase,
		Fields: map[string]any{
			"sku": "C100-R", "housing": "rugged",
		}}))

	// Document composed from reusable entries.
	doc := mustGUID(svc.CreateArtifact(ctx, repo.CreateArtifactParams{
		Kind: "document", Folder: "engineering", Type: "spec",
		Title: "System Specification"}))
	sections := fmt.Sprintf(`[
  {"id": "s1", "heading": "Introduction", "blocks": [
    {"type": "text", "text": "This specification defines the controller platform. It composes reusable requirement entries into a reviewable document."}
  ]},
  {"id": "s2", "heading": "Boot behavior", "blocks": [
    {"type": "text", "text": "The following requirement governs startup:"},
    {"type": "entryRef", "guid": "%s"}
  ], "children": [
    {"id": "s2a", "heading": "Verification", "blocks": [
      {"type": "text", "text": "Verified by the cold-boot measurement:"},
      {"type": "entryRef", "guid": "%s"}
    ]}
  ]},
  {"id": "s3", "heading": "Security", "blocks": [
    {"type": "entryRef", "guid": "%s"},
    {"type": "text", "text": "Signature verification is mandatory for all delivery channels."}
  ]}
]`, req1, tc1, req2)
	must(svc.UpdateArtifact(ctx, doc, repo.UpdateArtifactParams{Sections: []byte(sections)}))

	// Semantic relationships.
	_ = mustGUID(svc.CreateLink(ctx, repo.CreateLinkParams{Type: "satisfies", Source: req1, Target: tc1}))
	_ = mustGUID(svc.CreateLink(ctx, repo.CreateLinkParams{Type: "satisfies", Source: req2, Target: tc2}))
	_ = mustGUID(svc.CreateLink(ctx, repo.CreateLinkParams{Type: "parent", Source: req3, Target: req1}))

	// Workflow lifecycle.
	must(svc.WorkflowTransition(ctx, req1, "review", "in_review", ""))
	must(svc.WorkflowTransition(ctx, req2, "review", "in_review", ""))
	must(svc.WorkflowTransition(ctx, req2, "review", "approved", ""))

	// Collaboration.
	c1 := mustGUID(svc.CreateComment(ctx, repo.CreateCommentParams{
		Subject: req1, Author: "alice", Text: "Is 3 seconds measured from power-good or from reset release?"}))
	_ = mustGUID(svc.CreateComment(ctx, repo.CreateCommentParams{
		Subject: req1, Parent: c1, Author: "bob", Text: "From power-good — clarified in the next revision."}))
	_ = mustGUID(svc.CreateComment(ctx, repo.CreateCommentParams{
		Subject: doc, Author: "alice", Text: "Please add the safety chapter before review."}))

	stats, err := db.GetStats(ctx)
	must(err)
	log.Printf("seeded: %+v", stats)
	log.Printf("demo GUIDs: req1=%s doc=%s productVariant=%s", req1, doc, prodVariant)
}
