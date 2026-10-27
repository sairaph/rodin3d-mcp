package fbx

import "math"

// transformData is the loader's getTransformData of a model.
type transformData struct {
	inheritType                                      float64
	eulerOrder                                       string // "" when the RotationOrder maps to none
	translation, preRotation, rotation, postRotation *[3]float64
	scale, scalingOffset, scalingPivot               *[3]float64
	rotationOffset, rotationPivot                    *[3]float64
}

var eulerOrders = []string{"ZYX", "YZX", "XZY", "ZXY", "YXZ", "XYZ"}

// eulerOrder is getEulerOrder: FBX RotationOrder 0..5 to the three.js
// order; 6 (spherical) falls back to ZYX; anything else is none.
func eulerOrder(v any) string {
	f, ok := num(v)
	if !ok || math.IsNaN(f) || f == 0 {
		return eulerOrders[0]
	}
	if f == 6 {
		return eulerOrders[0]
	}
	if f == math.Trunc(f) && f >= 0 && f < 6 {
		return eulerOrders[int(f)]
	}
	return ""
}

// vec3 is Vector3.fromArray of a property value: missing entries are NaN.
func vec3(v any) *[3]float64 {
	if v == nil {
		return nil
	}
	out := [3]float64{math.NaN(), math.NaN(), math.NaN()}
	if list, ok := v.([]any); ok {
		for i := 0; i < 3 && i < len(list); i++ {
			if f, ok := num(list[i]); ok {
				out[i] = f
			}
		}
	}
	return &out
}

func transformDataOf(m *obj) *transformData {
	td := &transformData{}
	value := func(k string) (any, bool) {
		p, ok := m.vals[k]
		if !ok {
			return nil, false
		}
		if po, ok := p.(*obj); ok {
			v, _ := po.get("value")
			return v, true
		}
		return nil, true
	}
	if v, ok := value("InheritType"); ok {
		td.inheritType = math.NaN()
		switch x := v.(type) {
		case float64:
			td.inheritType = math.Trunc(x)
		case string:
			td.inheritType = parseIntJS(x)
		}
	}
	if v, ok := value("RotationOrder"); ok {
		td.eulerOrder = eulerOrder(v)
	} else {
		td.eulerOrder = eulerOrders[0]
	}
	set := func(k string, dst **[3]float64) {
		if v, ok := value(k); ok {
			*dst = vec3(v)
		}
	}
	set("Lcl_Translation", &td.translation)
	set("PreRotation", &td.preRotation)
	set("Lcl_Rotation", &td.rotation)
	set("PostRotation", &td.postRotation)
	set("Lcl_Scaling", &td.scale)
	set("ScalingOffset", &td.scalingOffset)
	set("ScalingPivot", &td.scalingPivot)
	set("RotationOffset", &td.rotationOffset)
	set("RotationPivot", &td.rotationPivot)
	return td
}

func v3(a *[3]float64) Vec3 { return Vec3{a[0], a[1], a[2]} }

func euler(a *[3]float64, order string) Mat4 {
	return RotationFromEuler(degToRad(a[0]), degToRad(a[1]), degToRad(a[2]), order)
}

// generateTransform is the loader's generateTransform (M): the local
// matrix of a model from its FBX transform properties and its parent's
// local and world matrices.
func generateTransform(t *transformData, parentMatrix, parentWorld Mat4) Mat4 {
	lT, lPre, lR, lPost := Identity(), Identity(), Identity(), Identity()
	lS, lSP, lSO, lRO, lRP := Identity(), Identity(), Identity(), Identity(), Identity()
	inherit := t.inheritType
	if math.IsNaN(inherit) {
		inherit = 0
	}
	if t.translation != nil {
		lT = Translation(v3(t.translation))
	}
	const def = "ZYX"
	if t.preRotation != nil {
		lPre = euler(t.preRotation, def)
	}
	if t.rotation != nil {
		order := t.eulerOrder
		if order == "" {
			order = def
		}
		lR = euler(t.rotation, order)
	}
	if t.postRotation != nil {
		lPost = euler(t.postRotation, def).Invert()
	}
	if t.scale != nil {
		lS = Scaling(v3(t.scale))
	}
	if t.scalingOffset != nil {
		lSO = Translation(v3(t.scalingOffset))
	}
	if t.scalingPivot != nil {
		lSP = Translation(v3(t.scalingPivot))
	}
	if t.rotationOffset != nil {
		lRO = Translation(v3(t.rotationOffset))
	}
	if t.rotationPivot != nil {
		lRP = Translation(v3(t.rotationPivot))
	}
	lParentLX, lParentGX := parentMatrix, parentWorld
	lLRM := Mul(Mul(lPre, lR), lPost)
	lParentGRM := lParentGX.ExtractRotation()
	lParentTM := lParentGX.CopyPosition()
	lParentGRSM := Mul(lParentTM.Invert(), lParentGX)
	lParentGSM := Mul(lParentGRM.Invert(), lParentGRSM)
	lLSM := lS
	var lGlobalRS Mat4
	switch inherit {
	case 0:
		lGlobalRS = Mul(Mul(Mul(lParentGRM, lLRM), lParentGSM), lLSM)
	case 1:
		lGlobalRS = Mul(Mul(Mul(lParentGRM, lParentGSM), lLRM), lLSM)
	default:
		lParentLSM := Scaling(lParentLX.ScaleOf()).Invert()
		lParentGSMNoLocal := Mul(lParentGSM, lParentLSM)
		lGlobalRS = Mul(Mul(Mul(lParentGRM, lLRM), lParentGSMNoLocal), lLSM)
	}
	lRPInv, lSPInv := lRP.Invert(), lSP.Invert()
	lTransform := lT
	for _, m := range []Mat4{lRO, lRP, lPre, lR, lPost, lRPInv, lSO, lSP, lS, lSPInv} {
		lTransform = Mul(lTransform, m)
	}
	lLocalTWithAllPivotAndOffsetInfo := lTransform.CopyPosition()
	lGlobalTranslation := Mul(lParentGX, lLocalTWithAllPivotAndOffsetInfo)
	lGlobalT := lGlobalTranslation.CopyPosition()
	out := Mul(lGlobalT, lGlobalRS)
	return Mul(lParentGX.Invert(), out)
}
