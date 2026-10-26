// Package assets lists the server images of a task per stage and mode, and
// keeps them in the cache folder (cache/<task>/<step>-<version>/<mode>.<ext>)
// so a preview does not download the same file twice.
package assets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sairaph/rodin3d-mcp/internal/domain"
	"github.com/sairaph/rodin3d-mcp/internal/rodin"
	"github.com/sairaph/rodin3d-mcp/internal/stage"
	"github.com/sairaph/rodin3d-mcp/internal/store"
)

// Preview stages (get_preview's stage argument; material covers the
// material draft and the confirmed material).
const (
	StageGeometryDraft = stage.GeometryDraft
	StageGeometry      = stage.Geometry
	StageMaterial      = stage.Material
)

// Stages in pipeline order.
var Stages = []string{StageGeometryDraft, StageGeometry, StageMaterial}

// Server image modes. The poster and the geometry and material images are
// the server's renders of the model; the texture_* images are the material's
// texture maps (flat UV atlases), not views of the model.
const (
	ModePoster         = "poster"
	ModeGeometryImage  = "geometry_image"
	ModeMaterialImage  = "material_image"
	ModeTextureDiffuse = "texture_diffuse"
	ModeTexturePBR     = "texture_pbr"
	ModeTextureNormal  = "texture_normal"
	ModeTextureShaded  = "texture_shaded"
)

// Modes of server images, per stage.
var Modes = map[string][]string{
	StageGeometryDraft: {ModePoster},
	StageGeometry:      {ModePoster, ModeGeometryImage},
	StageMaterial:      {ModePoster, ModeGeometryImage, ModeMaterialImage, ModeTextureDiffuse, ModeTexturePBR, ModeTextureNormal, ModeTextureShaded},
}

// AllModes lists every server image mode in display order.
var AllModes = []string{ModePoster, ModeGeometryImage, ModeMaterialImage, ModeTextureDiffuse, ModeTexturePBR, ModeTextureNormal, ModeTextureShaded}

// IsTextureMap reports whether a server image mode is a texture map.
func IsTextureMap(mode string) bool { return strings.HasPrefix(mode, "texture_") }

// Mesh files of a stage (Asset.Mode of MeshAsset): the geometry draft's
// Draco file and the GLB of the confirmed geometry or the material.
const (
	MeshDraft = "draft_mesh"
	MeshModel = "model_mesh"
)

// Asset is one server file of a task: an image, or a stage mesh.
type Asset struct {
	Stage   string // StageGeometryDraft | StageGeometry | StageMaterial
	Mode    string // a server image mode, MeshDraft or MeshModel
	Step    string // card step the file belongs to
	Version string // geometry version (v2) or material reference/version (ALL-v1)
	URL     string // signed address; never shown
}

// Ext is the file extension of the asset (".webp", ".jpg", ...).
func (a Asset) Ext() string {
	u := a.URL
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	ext := strings.ToLower(path.Ext(u))
	if ext == "" || len(ext) > 6 {
		return ".img"
	}
	return ext
}

// PreviewStage maps a task's stage to the preview stage that shows it.
func PreviewStage(taskStage string) string {
	switch taskStage {
	case stage.MaterialDraft, stage.Material:
		return StageMaterial
	case stage.Geometry:
		return StageGeometry
	}
	return StageGeometryDraft
}

// History is the version history Index can use; either map may be nil.
type History struct {
	Geometry map[string]rodin.GeometryVersion
	Material map[string]map[string]rodin.MaterialVersion
}

// Index lists the server images of card. Assets of earlier stages come from
// hist; without it only the images on the card itself are listed.
func Index(card *rodin.Card, hist History) []Asset {
	var out []Asset
	add := func(stg, mode, step, version, u string) {
		if u != "" {
			out = append(out, Asset{Stage: stg, Mode: mode, Step: step, Version: version, URL: u})
		}
	}
	st := stage.Of(card.Step, card.UserState, int(card.PendingJobs))
	geoVersion := firstNonEmpty(card.Confirmed("model_version"), card.Setting("version"), LatestVersion(keys(hist.Geometry)))

	// Geometry draft.
	switch st.Stage {
	case stage.GeometryDraft:
		add(StageGeometryDraft, ModePoster, rodin.StepModelGenerate, geoVersion, card.Poster["preview_image"])
	default:
		if v, ok := hist.Geometry[geoVersion]; ok {
			add(StageGeometryDraft, ModePoster, rodin.StepModelGenerate, geoVersion, v.PreviewCover)
		}
	}
	if st.Stage == stage.GeometryDraft {
		return out
	}

	// Geometry confirmed.
	if st.Stage == stage.Geometry {
		add(StageGeometry, ModePoster, rodin.StepModelRefine, geoVersion, card.Poster["preview_image"])
	}
	add(StageGeometry, ModeGeometryImage, rodin.StepModelRefine, geoVersion, card.Poster["geometry_image"])
	if st.Stage == stage.Geometry {
		return out
	}

	// Material (draft or confirmed).
	m := materialOf(card, hist)
	add(StageMaterial, ModePoster, m.step, m.cardTag, card.Poster["preview_image"])
	add(StageMaterial, ModeGeometryImage, rodin.StepModelRefine, geoVersion, card.Poster["geometry_image"])
	m.pick(add, ModeMaterialImage, card.Poster["material_image"], m.version.PreviewImage)
	m.pick(add, ModeTextureDiffuse, card.Preview["texture_diffuse"], m.version.TextureDiffuse)
	m.pick(add, ModeTexturePBR, card.Preview["texture_pbr"], m.version.TexturePBR)
	m.pick(add, ModeTextureNormal, card.Preview["texture_normal"], m.version.TextureNormal)
	m.pick(add, ModeTextureShaded, card.Preview["texture_shaded"], m.version.TextureShaded)
	return out
}

// material is where a material-stage card's files come from.
type material struct {
	step             string // the card's step, for files on the card
	cardTag, histTag string
	version          rodin.MaterialVersion // the shown version from the history (zero without it)
}

// materialOf finds the card's material version. Files on the card are the
// current ones (tag: the reference); files from the history carry their
// version. The cache path then does not depend on whether the history was
// read.
func materialOf(card *rodin.Card, hist History) material {
	ref := MaterialRef(card, hist)
	m := material{step: card.Step, cardTag: ref}
	matVersion := ""
	if versions := hist.Material[ref]; len(versions) > 0 {
		matVersion = firstNonEmpty(card.Confirmed("texture_version"), LatestVersion(keys(versions)))
		m.version = versions[matVersion]
	}
	m.histTag = strings.Trim(ref+"-"+matVersion, "-")
	return m
}

// pick adds the material file from the card, or else from the history.
func (m material) pick(add func(stg, mode, step, version, u string), mode, fromCard, fromHist string) {
	if fromCard != "" {
		add(StageMaterial, mode, m.step, m.cardTag, fromCard)
	} else {
		add(StageMaterial, mode, rodin.StepTextureGenerate, m.histTag, fromHist)
	}
}

// MeshAsset returns the mesh file a stage is rendered from: for the geometry
// draft its Draco file (the card's preview_sdf, or the confirmed version's
// draft from hist on a later card); for the confirmed geometry and the
// material the card's GLB (model). A card past the geometry stage carries
// only the material GLB, which holds the confirmed geometry with texture
// coordinates, so the geometry stage of such a card is rendered from it.
func MeshAsset(card *rodin.Card, hist History, stg string) (Asset, bool) {
	st := stage.Of(card.Step, card.UserState, int(card.PendingJobs))
	geoVersion := firstNonEmpty(card.Confirmed("model_version"), card.Setting("version"), LatestVersion(keys(hist.Geometry)))
	ok := func(a Asset) (Asset, bool) { return a, a.URL != "" }
	switch stg {
	case StageGeometryDraft:
		u := card.Preview["preview_sdf"]
		if u == "" && st.Stage != stage.GeometryDraft {
			u = hist.Geometry[geoVersion].PreviewDRC
		}
		return ok(Asset{Stage: stg, Mode: MeshDraft, Step: rodin.StepModelGenerate, Version: geoVersion, URL: u})
	case StageGeometry:
		switch st.Stage {
		case stage.Geometry:
			return ok(Asset{Stage: stg, Mode: MeshModel, Step: rodin.StepModelRefine, Version: geoVersion, URL: card.Preview["model"]})
		case stage.MaterialDraft, stage.Material:
			a, found := MeshAsset(card, hist, StageMaterial)
			a.Stage = stg
			return a, found
		}
	case StageMaterial:
		if st.Stage != stage.MaterialDraft && st.Stage != stage.Material {
			return Asset{}, false
		}
		var a Asset
		m := materialOf(card, hist)
		m.pick(func(_, mode, step, version, u string) {
			a = Asset{Stage: stg, Mode: mode, Step: step, Version: version, URL: u}
		}, MeshModel, card.Preview["model"], m.version.Model)
		return ok(a)
	}
	return Asset{}, false
}

// StageInputs is the Asset.Stage of a task's input images.
const StageInputs = "inputs"

// Inputs lists the task's input images in upload order (the order its
// image_label and view_weights refer to; names sort by the upload index the
// service gives them), with their names. Asset.Mode is "input_" plus the
// name without its extension; the files are cached under inputs/.
func Inputs(card *rodin.Card, order func([]string)) (names []string, list []Asset) {
	names = make([]string, 0, len(card.Info.MeshInputImages))
	for n := range card.Info.MeshInputImages {
		names = append(names, n)
	}
	order(names)
	list = make([]Asset, 0, len(names))
	for _, n := range names {
		base := strings.TrimSuffix(n, path.Ext(n))
		list = append(list, Asset{Stage: StageInputs, Mode: "input_" + unsafeName.Replace(base), Step: StageInputs, URL: card.Info.MeshInputImages[n]})
	}
	return names, list
}

// GeometryDraftAt returns the draft mesh and poster of geometry version v
// from the history (empty addresses when the history lacks them).
func GeometryDraftAt(hist History, v string) (mesh, poster Asset) {
	gv := hist.Geometry[v]
	mesh = Asset{Stage: StageGeometryDraft, Mode: MeshDraft, Step: rodin.StepModelGenerate, Version: v, URL: gv.PreviewDRC}
	poster = Asset{Stage: StageGeometryDraft, Mode: ModePoster, Step: rodin.StepModelGenerate, Version: v, URL: gv.PreviewCover}
	return mesh, poster
}

// MaterialRef is the material history reference a card's material belongs
// to: its reference_image setting ("ALL" or an input image name); when that
// is empty and the history has exactly one reference, that one. Index and
// the redo counter both use it, so they never disagree.
func MaterialRef(card *rodin.Card, hist History) string {
	if ref := card.Setting("reference_image"); ref != "" {
		return ref
	}
	if len(hist.Material) == 1 {
		for r := range hist.Material {
			return r
		}
	}
	return ""
}

// Find returns the asset for stage and mode.
func Find(list []Asset, stg, mode string) (Asset, bool) {
	for _, a := range list {
		if a.Stage == stg && a.Mode == mode {
			return a, true
		}
	}
	return Asset{}, false
}

// Available returns the modes list holds for stage, in display order.
func Available(list []Asset, stg string) []string {
	var out []string
	for _, m := range AllModes {
		if _, ok := Find(list, stg, m); ok {
			out = append(out, m)
		}
	}
	return out
}

// NeedsHistory reports whether Index needs the version history for the
// assets of stg on a card in stage taskStage.
func NeedsHistory(taskStage, stg string) bool {
	switch stg {
	case StageGeometryDraft:
		return taskStage != stage.GeometryDraft
	case StageMaterial:
		return taskStage == stage.MaterialDraft || taskStage == stage.Material
	}
	return false
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// SortVersions orders version keys v1, v2, ..., v10 numerically; keys that
// are not of that form sort after, by name.
func SortVersions(vs []string) []string {
	out := append([]string(nil), vs...)
	num := func(v string) int {
		if n, err := strconv.Atoi(strings.TrimPrefix(v, "v")); err == nil && strings.HasPrefix(v, "v") {
			return n
		}
		return 1 << 30
	}
	sort.SliceStable(out, func(i, j int) bool {
		ni, nj := num(out[i]), num(out[j])
		if ni != nj {
			return ni < nj
		}
		return out[i] < out[j]
	})
	return out
}

// LatestVersion returns the highest version key ("" for none).
func LatestVersion(vs []string) string {
	s := SortVersions(vs)
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

// Cache keeps fetched assets on disk.
type Cache struct {
	dir    string
	store  *store.Store
	client *rodin.Client
}

// NewCache returns the cache under dir.
func NewCache(dir string, st *store.Store, client *rodin.Client) *Cache {
	return &Cache{dir: dir, store: st, client: client}
}

// maxAsset bounds one cached image.
const maxAsset = 64 << 20

// sourceKey hashes the asset's address without its signature.
func sourceKey(a Asset) string {
	sum := sha256.Sum256([]byte(rodin.SourceKey(a.URL)))
	return hex.EncodeToString(sum[:8])
}

func kind(a Asset) string { return "cache:" + a.Mode }

var unsafeName = strings.NewReplacer("/", "_", "\\", "_", ":", "_", "..", "_")

// Path is where asset a of task uuid is stored.
func (c *Cache) Path(uuid string, a Asset) string {
	dir := unsafeName.Replace(strings.Trim(a.Step+"-"+a.Version, "-"))
	return filepath.Join(c.dir, unsafeName.Replace(uuid), dir, a.Mode+a.Ext())
}

// Get returns the file of asset a, fetching it when it is missing, its
// source changed, or the host reports a newer version: a stored copy is
// revalidated with a conditional request (its ETag or Last-Modified), so a
// file rewritten at the same address is noticed. When the host cannot be
// reached a stored copy is returned as it is. fetched reports a download.
func (c *Cache) Get(ctx context.Context, uuid string, a Asset) (file string, fetched bool, err error) {
	file = c.Path(uuid, a)
	key := sourceKey(a)
	cached, validator := false, ""
	if rows, err := c.store.Files(uuid, kind(a)); err == nil {
		for _, f := range rows {
			if f.Path == file && f.SourceKey == key {
				if fi, err := os.Stat(file); err == nil && fi.Size() == f.Bytes {
					cached, validator = true, f.Validator
				}
			}
		}
	}
	if a.URL == "" {
		if cached {
			return file, false, nil
		}
		return "", false, errors.New("the asset has no address")
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return "", false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".fetch-*")
	if err != nil {
		return "", false, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	res, err := c.client.FetchIf(ctx, a.URL, io.MultiWriter(tmp, h), maxAsset, validator)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil && cached:
		return file, false, nil // offline or a passing failure: the stored copy serves
	case err != nil:
		return "", false, fmt.Errorf("download the %s %s image: %w", a.Stage, a.Mode, err)
	case res.NotModified:
		return file, false, nil
	}
	if err := domain.ReplaceFile(tmp.Name(), file); err != nil {
		return "", false, err
	}
	if err := c.store.RecordFile(store.File{UUID: uuid, Kind: kind(a), Path: file, Bytes: res.Bytes,
		SHA256: hex.EncodeToString(h.Sum(nil)), SourceKey: key, Validator: res.Validator}); err != nil {
		return "", false, err
	}
	return file, true, nil
}

// Cached returns the cached file of a without fetching ("" when absent or
// stale). It works from a stored card, whose addresses have no signature.
func (c *Cache) Cached(uuid string, a Asset) string {
	file := c.Path(uuid, a)
	key := sourceKey(a)
	rows, err := c.store.Files(uuid, kind(a))
	if err != nil {
		return ""
	}
	for _, f := range rows {
		if f.Path == file && f.SourceKey == key {
			if fi, err := os.Stat(file); err == nil && fi.Size() == f.Bytes {
				return file
			}
		}
	}
	return ""
}
