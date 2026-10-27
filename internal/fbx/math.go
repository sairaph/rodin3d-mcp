package fbx

import "math"

// Mat4, Vec3 and Quat port the three.js math the web's FBX import runs on
// (Matrix4, Vector3, Quaternion, Euler of the three build in the web
// bundle), so the numbers come out the same. Mat4 is column-major, as
// three.js keeps its elements and sends them (toArray).

// Mat4 is a 4x4 matrix in column-major order.
type Mat4 [16]float64

// Vec3 is a 3D vector.
type Vec3 struct{ X, Y, Z float64 }

// Quat is a unit quaternion (x, y, z, w).
type Quat struct{ X, Y, Z, W float64 }

// Identity returns the identity matrix.
func Identity() Mat4 { return Mat4{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1} }

// Mul returns a * b (three.js multiplyMatrices).
func Mul(a, b Mat4) Mat4 {
	var o Mat4
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			o[c*4+r] = a[r]*b[c*4] + a[4+r]*b[c*4+1] + a[8+r]*b[c*4+2] + a[12+r]*b[c*4+3]
		}
	}
	return o
}

// Translation returns a matrix that moves by v (makeTranslation; also
// identity.setPosition(v)).
func Translation(v Vec3) Mat4 {
	m := Identity()
	m[12], m[13], m[14] = v.X, v.Y, v.Z
	return m
}

// Scaling returns identity.scale(v).
func Scaling(v Vec3) Mat4 {
	m := Identity()
	m[0], m[5], m[10] = v.X, v.Y, v.Z
	return m
}

// Invert returns the inverse of m; a singular m gives the zero matrix, as
// three.js does.
func (m Mat4) Invert() Mat4 {
	n11, n21, n31, n41 := m[0], m[1], m[2], m[3]
	n12, n22, n32, n42 := m[4], m[5], m[6], m[7]
	n13, n23, n33, n43 := m[8], m[9], m[10], m[11]
	n14, n24, n34, n44 := m[12], m[13], m[14], m[15]
	t11 := n23*n34*n42 - n24*n33*n42 + n24*n32*n43 - n22*n34*n43 - n23*n32*n44 + n22*n33*n44
	t12 := n14*n33*n42 - n13*n34*n42 - n14*n32*n43 + n12*n34*n43 + n13*n32*n44 - n12*n33*n44
	t13 := n13*n24*n42 - n14*n23*n42 + n14*n22*n43 - n12*n24*n43 - n13*n22*n44 + n12*n23*n44
	t14 := n14*n23*n32 - n13*n24*n32 - n14*n22*n33 + n12*n24*n33 + n13*n22*n34 - n12*n23*n34
	det := n11*t11 + n21*t12 + n31*t13 + n41*t14
	if det == 0 {
		return Mat4{}
	}
	d := 1 / det
	var o Mat4
	o[0] = t11 * d
	o[1] = (n24*n33*n41 - n23*n34*n41 - n24*n31*n43 + n21*n34*n43 + n23*n31*n44 - n21*n33*n44) * d
	o[2] = (n22*n34*n41 - n24*n32*n41 + n24*n31*n42 - n21*n34*n42 - n22*n31*n44 + n21*n32*n44) * d
	o[3] = (n23*n32*n41 - n22*n33*n41 - n23*n31*n42 + n21*n33*n42 + n22*n31*n43 - n21*n32*n43) * d
	o[4] = t12 * d
	o[5] = (n13*n34*n41 - n14*n33*n41 + n14*n31*n43 - n11*n34*n43 - n13*n31*n44 + n11*n33*n44) * d
	o[6] = (n14*n32*n41 - n12*n34*n41 - n14*n31*n42 + n11*n34*n42 + n12*n31*n44 - n11*n32*n44) * d
	o[7] = (n12*n33*n41 - n13*n32*n41 + n13*n31*n42 - n11*n33*n42 - n12*n31*n43 + n11*n32*n43) * d
	o[8] = t13 * d
	o[9] = (n14*n23*n41 - n13*n24*n41 - n14*n21*n43 + n11*n24*n43 + n13*n21*n44 - n11*n23*n44) * d
	o[10] = (n12*n24*n41 - n14*n22*n41 + n14*n21*n42 - n11*n24*n42 - n12*n21*n44 + n11*n22*n44) * d
	o[11] = (n13*n22*n41 - n12*n23*n41 - n13*n21*n42 + n11*n23*n42 + n12*n21*n43 - n11*n22*n43) * d
	o[12] = t14 * d
	o[13] = (n13*n24*n31 - n14*n23*n31 + n14*n21*n33 - n11*n24*n33 - n13*n21*n34 + n11*n23*n34) * d
	o[14] = (n14*n22*n31 - n12*n24*n31 - n14*n21*n32 + n11*n24*n32 + n12*n21*n34 - n11*n22*n34) * d
	o[15] = (n12*n23*n31 - n13*n22*n31 + n13*n21*n32 - n11*n23*n32 - n12*n21*n33 + n11*n22*n33) * d
	return o
}

// Determinant is three.js Matrix4.determinant.
func (m Mat4) Determinant() float64 {
	n11, n12, n13, n14 := m[0], m[4], m[8], m[12]
	n21, n22, n23, n24 := m[1], m[5], m[9], m[13]
	n31, n32, n33, n34 := m[2], m[6], m[10], m[14]
	n41, n42, n43, n44 := m[3], m[7], m[11], m[15]
	return n41*(+n14*n23*n32-n13*n24*n32-n14*n22*n33+n12*n24*n33+n13*n22*n34-n12*n23*n34) +
		n42*(+n11*n23*n34-n11*n24*n33+n14*n21*n33-n13*n21*n34+n13*n24*n31-n14*n23*n31) +
		n43*(+n11*n24*n32-n11*n22*n34-n14*n21*n32+n12*n21*n34+n14*n22*n31-n12*n24*n31) +
		n44*(-n13*n22*n31-n11*n23*n32+n11*n22*n33+n13*n21*n32-n12*n21*n33+n12*n23*n31)
}

func (v Vec3) Length() float64 { return math.Sqrt(v.X*v.X + v.Y*v.Y + v.Z*v.Z) }

func (v Vec3) LengthSq() float64 { return v.X*v.X + v.Y*v.Y + v.Z*v.Z }

func (v Vec3) Sub(w Vec3) Vec3 { return Vec3{v.X - w.X, v.Y - w.Y, v.Z - w.Z} }

func (v Vec3) Scale(s float64) Vec3 { return Vec3{v.X * s, v.Y * s, v.Z * s} }

func (v Vec3) Dot(w Vec3) float64 { return v.X*w.X + v.Y*w.Y + v.Z*w.Z }

// AddScaled is v + w*s (addScaledVector).
func (v Vec3) AddScaled(w Vec3, s float64) Vec3 { return Vec3{v.X + w.X*s, v.Y + w.Y*s, v.Z + w.Z*s} }

// Cross is v x w (crossVectors).
func (v Vec3) Cross(w Vec3) Vec3 {
	return Vec3{v.Y*w.Z - v.Z*w.Y, v.Z*w.X - v.X*w.Z, v.X*w.Y - v.Y*w.X}
}

// Normalize is three.js normalize: divide by the length, or by 1 when it
// is 0.
func (v Vec3) Normalize() Vec3 {
	l := v.Length()
	if l == 0 {
		l = 1
	}
	return v.Scale(1 / l)
}

// DistanceTo is distanceTo.
func (v Vec3) DistanceTo(w Vec3) float64 { return v.Sub(w).Length() }

// Position returns the translation of m (setFromMatrixPosition).
func (m Mat4) Position() Vec3 { return Vec3{m[12], m[13], m[14]} }

// ApplyMat4 is v.applyMatrix4(m).
func (v Vec3) ApplyMat4(m Mat4) Vec3 {
	w := 1 / (m[3]*v.X + m[7]*v.Y + m[11]*v.Z + m[15])
	return Vec3{
		(m[0]*v.X + m[4]*v.Y + m[8]*v.Z + m[12]) * w,
		(m[1]*v.X + m[5]*v.Y + m[9]*v.Z + m[13]) * w,
		(m[2]*v.X + m[6]*v.Y + m[10]*v.Z + m[14]) * w,
	}
}

// TransformDirection is v.transformDirection(m): the upper 3x3 applied,
// then normalized.
func (v Vec3) TransformDirection(m Mat4) Vec3 {
	return Vec3{
		m[0]*v.X + m[4]*v.Y + m[8]*v.Z,
		m[1]*v.X + m[5]*v.Y + m[9]*v.Z,
		m[2]*v.X + m[6]*v.Y + m[10]*v.Z,
	}.Normalize()
}

// ApplyQuat is v.applyQuaternion(q).
func (v Vec3) ApplyQuat(q Quat) Vec3 {
	tx := 2 * (q.Y*v.Z - q.Z*v.Y)
	ty := 2 * (q.Z*v.X - q.X*v.Z)
	tz := 2 * (q.X*v.Y - q.Y*v.X)
	return Vec3{
		v.X + q.W*tx + q.Y*tz - q.Z*ty,
		v.Y + q.W*ty + q.Z*tx - q.X*tz,
		v.Z + q.W*tz + q.X*ty - q.Y*tx,
	}
}

// ScaleOf is setFromMatrixScale: the lengths of the first three columns.
func (m Mat4) ScaleOf() Vec3 {
	return Vec3{Vec3{m[0], m[1], m[2]}.Length(), Vec3{m[4], m[5], m[6]}.Length(), Vec3{m[8], m[9], m[10]}.Length()}
}

// ExtractRotation is three.js extractRotation: the 3x3 columns divided by
// their lengths, no translation.
func (m Mat4) ExtractRotation() Mat4 {
	s := m.ScaleOf()
	inv := func(x float64) float64 { return 1 / x }
	sx, sy, sz := inv(s.X), inv(s.Y), inv(s.Z)
	return Mat4{
		m[0] * sx, m[1] * sx, m[2] * sx, 0,
		m[4] * sy, m[5] * sy, m[6] * sy, 0,
		m[8] * sz, m[9] * sz, m[10] * sz, 0,
		0, 0, 0, 1,
	}
}

// CopyPosition is identity.copyPosition(m).
func (m Mat4) CopyPosition() Mat4 { return Translation(m.Position()) }

// Compose is three.js Matrix4.compose.
func Compose(p Vec3, q Quat, s Vec3) Mat4 {
	x, y, z, w := q.X, q.Y, q.Z, q.W
	x2, y2, z2 := x+x, y+y, z+z
	xx, xy, xz := x*x2, x*y2, x*z2
	yy, yz, zz := y*y2, y*z2, z*z2
	wx, wy, wz := w*x2, w*y2, w*z2
	return Mat4{
		(1 - (yy + zz)) * s.X, (xy + wz) * s.X, (xz - wy) * s.X, 0,
		(xy - wz) * s.Y, (1 - (xx + zz)) * s.Y, (yz + wx) * s.Y, 0,
		(xz + wy) * s.Z, (yz - wx) * s.Z, (1 - (xx + yy)) * s.Z, 0,
		p.X, p.Y, p.Z, 1,
	}
}

// Decompose is three.js Matrix4.decompose.
func (m Mat4) Decompose() (Vec3, Quat, Vec3) {
	sx := Vec3{m[0], m[1], m[2]}.Length()
	sy := Vec3{m[4], m[5], m[6]}.Length()
	sz := Vec3{m[8], m[9], m[10]}.Length()
	if m.Determinant() < 0 {
		sx = -sx
	}
	pos := Vec3{m[12], m[13], m[14]}
	r := m
	ix, iy, iz := 1/sx, 1/sy, 1/sz
	r[0], r[1], r[2] = r[0]*ix, r[1]*ix, r[2]*ix
	r[4], r[5], r[6] = r[4]*iy, r[5]*iy, r[6]*iy
	r[8], r[9], r[10] = r[8]*iz, r[9]*iz, r[10]*iz
	return pos, QuatFromRotation(r), Vec3{sx, sy, sz}
}

// QuatFromRotation is setFromRotationMatrix (the upper 3x3 must be a pure
// rotation).
func QuatFromRotation(m Mat4) Quat {
	m11, m12, m13 := m[0], m[4], m[8]
	m21, m22, m23 := m[1], m[5], m[9]
	m31, m32, m33 := m[2], m[6], m[10]
	trace := m11 + m22 + m33
	switch {
	case trace > 0:
		s := 0.5 / math.Sqrt(trace+1.0)
		return Quat{(m32 - m23) * s, (m13 - m31) * s, (m21 - m12) * s, 0.25 / s}
	case m11 > m22 && m11 > m33:
		s := 2.0 * math.Sqrt(1.0+m11-m22-m33)
		return Quat{0.25 * s, (m12 + m21) / s, (m13 + m31) / s, (m32 - m23) / s}
	case m22 > m33:
		s := 2.0 * math.Sqrt(1.0+m22-m11-m33)
		return Quat{(m12 + m21) / s, 0.25 * s, (m23 + m32) / s, (m13 - m31) / s}
	}
	s := 2.0 * math.Sqrt(1.0+m33-m11-m22)
	return Quat{(m13 + m31) / s, (m23 + m32) / s, 0.25 * s, (m21 - m12) / s}
}

// Basis is makeBasis(x, y, z): the vectors as columns.
func Basis(x, y, z Vec3) Mat4 {
	return Mat4{x.X, x.Y, x.Z, 0, y.X, y.Y, y.Z, 0, z.X, z.Y, z.Z, 0, 0, 0, 0, 1}
}

// RotationFromEuler is makeRotationFromEuler for angles in radians and a
// three.js order (XYZ, YXZ, ZXY, ZYX, YZX, XZY).
func RotationFromEuler(x, y, z float64, order string) Mat4 {
	a, b := math.Cos(x), math.Sin(x)
	c, d := math.Cos(y), math.Sin(y)
	e, f := math.Cos(z), math.Sin(z)
	m := Identity()
	set := func(r1, r2, r3 [3]float64) {
		m[0], m[4], m[8] = r1[0], r1[1], r1[2]
		m[1], m[5], m[9] = r2[0], r2[1], r2[2]
		m[2], m[6], m[10] = r3[0], r3[1], r3[2]
	}
	switch order {
	case "XYZ":
		ae, af, be, bf := a*e, a*f, b*e, b*f
		set([3]float64{c * e, -c * f, d}, [3]float64{af + be*d, ae - bf*d, -b * c}, [3]float64{bf - ae*d, be + af*d, a * c})
	case "YXZ":
		ce, cf, de, df := c*e, c*f, d*e, d*f
		set([3]float64{ce + df*b, de*b - cf, a * d}, [3]float64{a * f, a * e, -b}, [3]float64{cf*b - de, df + ce*b, a * c})
	case "ZXY":
		ce, cf, de, df := c*e, c*f, d*e, d*f
		set([3]float64{ce - df*b, -a * f, de + cf*b}, [3]float64{cf + de*b, a * e, df - ce*b}, [3]float64{-a * d, b, a * c})
	case "ZYX":
		ae, af, be, bf := a*e, a*f, b*e, b*f
		set([3]float64{c * e, be*d - af, ae*d + bf}, [3]float64{c * f, bf*d + ae, af*d - be}, [3]float64{-d, b * c, a * c})
	case "YZX":
		ac, ad, bc, bd := a*c, a*d, b*c, b*d
		set([3]float64{c * e, bd - ac*f, bc*f + ad}, [3]float64{f, a * e, -b * e}, [3]float64{-d * e, ad*f + bc, ac - bd*f})
	case "XZY":
		ac, ad, bc, bd := a*c, a*d, b*c, b*d
		set([3]float64{c * e, -f, d * e}, [3]float64{ac*f + bd, a * e, ad*f - bc}, [3]float64{bc*f - ad, b * e, bd*f + ac})
	}
	return m
}

// Normalize is Quaternion.normalize.
func (q Quat) Normalize() Quat {
	l := math.Sqrt(q.X*q.X + q.Y*q.Y + q.Z*q.Z + q.W*q.W)
	if l == 0 {
		return Quat{0, 0, 0, 1}
	}
	l = 1 / l
	return Quat{q.X * l, q.Y * l, q.Z * l, q.W * l}
}

// Mul is a.multiply(b) (a * b).
func (a Quat) Mul(b Quat) Quat {
	return Quat{
		a.X*b.W + a.W*b.X + a.Y*b.Z - a.Z*b.Y,
		a.Y*b.W + a.W*b.Y + a.Z*b.X - a.X*b.Z,
		a.Z*b.W + a.W*b.Z + a.X*b.Y - a.Y*b.X,
		a.W*b.W - a.X*b.X - a.Y*b.Y - a.Z*b.Z,
	}
}

// Invert is Quaternion.invert (the conjugate).
func (q Quat) Invert() Quat { return Quat{-q.X, -q.Y, -q.Z, q.W} }

// QuatFromUnitVectors is setFromUnitVectors(from, to).
func QuatFromUnitVectors(from, to Vec3) Quat {
	r := from.Dot(to) + 1
	var q Quat
	if r < 1e-8 { // opposite vectors: a half turn about an orthogonal axis (three r180)
		r = 0
		if math.Abs(from.X) > math.Abs(from.Z) {
			q = Quat{-from.Y, from.X, 0, r}
		} else {
			q = Quat{0, -from.Z, from.Y, r}
		}
	} else {
		q = Quat{from.Y*to.Z - from.Z*to.Y, from.Z*to.X - from.X*to.Z, from.X*to.Y - from.Y*to.X, r}
	}
	return q.Normalize()
}

// Finite reports whether every element is a finite number.
func (m Mat4) Finite() bool {
	for _, v := range m {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

func degToRad(d float64) float64 { return d * (math.Pi / 180) }
