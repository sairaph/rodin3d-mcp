package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"

	"github.com/sairaph/mcp-wizard/render"

	"github.com/sairaph/rodin3d-mcp/internal/price"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
)

// Agentic Mode (web builder at, main.fmt.js:146439; call site 10612): the
// server plans and builds the model from 1-5 images within a time budget.
// With N-gons on (the web's default) the request carries no route and the
// server picks the producer; with N-gons off it sends route "rodin", which
// asks for the high-poly producer. A high-poly result is an ordinary Rodin
// task that ends as a geometry draft (live 2026-10-27; the official MCP's
// run of 2026-10-26 went on to a pack, the web's does not); an n-gons result
// is a RodinAgentic task (type RodinAgentic, versions in
// rodin_agentic_history), not seen live.

// AgenticInput is the input of generate_agentic.
type AgenticInput struct {
	Images   []string // absolute image paths
	Prompt   string
	Budget   string // basic | standard | pro
	NGons    bool   // the web's N-gons switch (default on)
	Public   bool
	Expected *float64
	Wait     bool
}

type agenticDetail struct {
	Producer string         `yaml:"producer"`
	Budget   string         `yaml:"budget"`
	Route    string         `yaml:"route,omitempty"`
	Prompt   string         `yaml:"prompt"`
	Inputs   []agenticInput `yaml:"inputs"`
	Privacy  *privacyFront  `yaml:"privacy"`
}

// agenticInput is one image sent: Agentic takes no view tags or weights.
type agenticInput struct {
	File string `yaml:"file"`
	Sent string `yaml:"sent"`
}

// newRequestID returns a random UUID (version 4), the web's
// client_request_id.
func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// GenerateAgentic is generate_agentic: preprocess the first image (free,
// for the caption and height the web sends), submit task/rodin_agentic,
// make the task private, wait for it.
func (s *Service) GenerateAgentic(ctx context.Context, in AgenticInput) (Result, error) {
	if _, err := s.permit("generate_agentic"); err != nil {
		return Result{}, err
	}
	if _, err := s.signedIn(ctx); err != nil {
		return Result{}, err
	}
	budget := strings.ToLower(strings.TrimSpace(in.Budget))
	if !oneOf(budget, "basic", "standard", "pro") {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("budget %q is not basic, standard or pro.", in.Budget),
			"Pass basic (about 3 minutes), standard (about 5) or pro (about 10); quote_cost gives each price.")
	}
	list := make([]GenerateImage, len(in.Images))
	for i, p := range in.Images {
		list[i] = GenerateImage{Path: p}
	}
	images, err := prepareImages(list)
	if err != nil {
		return Result{}, err
	}
	if len([]rune(in.Prompt)) > maxPrompt {
		return Result{}, fail(render.CodeInvalidInput, fmt.Sprintf("prompt is %d characters; at most %d.", len([]rune(in.Prompt)), maxPrompt), "Shorten the prompt.")
	}
	sp, err := s.prepare(ctx, "generate_agentic", "", price.Input{Budget: budget}, in.Expected, true)
	if err != nil {
		return Result{}, err
	}
	if len(images) > 1 && !sp.info.HasPrivilege("RodinUseMultiImage") {
		return Result{}, &price.Gate{Reason: "2 or more images in one submit need the RodinUseMultiImage privilege (a paid plan)"}
	}
	pre, err := s.client.ImageProcess(ctx, images[0].upload.Name, images[0].upload.MIME, images[0].upload.Data, true)
	if err != nil {
		return Result{}, wrapf(err, "preprocess image 1 (%s)", images[0].file)
	}
	req := rodin.AgenticRequest{Prompt: strings.TrimSpace(in.Prompt), BudgetTier: budget, ClientRequestID: newRequestID()}
	if req.Prompt == "" {
		req.Prompt = strings.TrimSpace(pre.Prompt)
	}
	if h, err := strconv.ParseFloat(pre.Height.String(), 64); err == nil && h > 0 {
		req.Height = &h
	}
	if !in.NGons {
		req.Route = "rodin"
	}
	detail := agenticDetail{Budget: budget, Route: req.Route, Prompt: req.Prompt}
	for _, img := range images {
		req.Images = append(req.Images, img.upload)
		detail.Inputs = append(detail.Inputs, agenticInput{File: img.file, Sent: img.sent})
	}

	sp.open()
	reply, err := s.client.Agentic(ctx, req)
	if err != nil {
		sp.close(ctx, err)
		return Result{}, sendFailure("generate_agentic", "", err)
	}
	sp.uuid = reply.UUID
	detail.Producer = reply.Producer
	privacy, privacyNote := s.privacyOfNew(ctx, reply.UUID, in.Public)
	detail.Privacy = privacy
	set, err := s.settle(ctx, reply.UUID, stage.Status{}, reply.UUIDs(), in.Wait)
	sp.close(ctx, nil)
	if err != nil {
		return Result{}, wrapf(err, "the Agentic task %s was submitted; read its card", reply.UUID)
	}
	lead := fmt.Sprintf("Agentic task %s started (%s budget, producer %s).", reply.UUID, budget, orUnknown(reply.Producer))
	res := s.finishFlow(ctx, sp, set, lead, detail)
	switch reply.Producer {
	case "high-poly":
		// Live 2026-10-27: the card ran Created/Generating for about two
		// minutes and stopped at ModelGenerate/DonePreview, an ordinary
		// draft (the web then opens it in its workbench).
		res.Body += "\n\nThe high-poly producer makes an ordinary Rodin task: when the run ends it is a geometry draft like any other " +
			"(wait_task follows it), and confirm_geometry, generate_texture and export_model take it from there."
	case "n-gons":
		res.Body += "\n\nThe n-gons producer makes a RodinAgentic task, which no tool here acts on: get_task, wait_task and get_preview read its card."
	}
	if privacyNote != "" {
		res.Body += "\n\n" + privacyNote
	}
	return res, nil
}
