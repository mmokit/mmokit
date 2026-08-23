package spatial

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
)

// BucketKey identifies a bucket in the spatial hash grid.
type BucketKey struct {
	X, Y int32
}

// Entry stores an entity with its position, shape, and collision layer.
//
// X, Y and Z are WORLD-ABSOLUTE. Partitioning is horizontal-only, so unlike a
// component.Position — which is local to its base cell — Z carries no cell
// term and never will: there is no vertical cell boundary for one to be
// relative to.
//
// BUCKETS ARE 2D COLUMNS. bucketKey hashes X and Y alone, and Z is filtered
// inside the bucket scan rather than partitioning it. That is a deliberate
// choice, not an unfinished one — see bucketKey — and it has a property worth
// relying on: a spherical query over column buckets can only ever NARROW the
// result set relative to the old cylindrical test, never widen it.
//
// ENTRY.DEPTH IS A BOX EXTENT, and sits one grep away from CellID.Depth, which
// is a quadtree level and utterly unrelated. Zero for a sphere and zero
// throughout a 2D profile.
//
// Rot is the full unit quaternion, not a yaw. The planar narrow phase reads a
// yaw out of it because that is all a 2D OBB test can use; the three-axis test
// that replaces it needs the whole orientation, and a scalar Yaw field sitting
// beside a quaternion component.Rotation is a value someone eventually assigns
// the wrong thing to.
type Entry struct {
	Entity  ecs.Entity
	X, Y, Z float32
	Radius  float32 // authored bounding radius; see Bound
	Width   float32 // box extent along local X (forward), 0 for spheres
	Height  float32 // box extent along local Y (side), 0 for spheres
	Depth   float32 // box extent along local Z (up), 0 for spheres and in a 2D profile
	Rot     component.Rotation
	Layer   uint8
	Shape   component.ShapeKind
}

// Bound is the radius of the sphere containing this entry, and is what every
// broad-phase test measures against.
//
// A METHOD, not a stored field, and that is load-bearing. Register, Update and
// InsertTransient each take a caller-built Entry by value with no validation,
// so a stored bound is a field a hand-built entry leaves at zero — and a
// zero-bound entry is invisible to every broad-phase test, silently. Deriving
// it means an entry cannot be half-built. The compare is one predictable
// branch inside a bucket scan.
//
// For a sphere and a box it is Radius by construction, which is what makes
// "the 2D result set is unchanged" a test rather than a claim.
func (e *Entry) Bound() float32 { return e.Radius }

// bucket holds both tracked (persistent) and transient (per-tick) entries.
type bucket struct {
	tracked   []Entry
	transient []Entry
}

// trackInfo stores an entity's current bucket and index for O(1) operations.
type trackInfo struct {
	bucket *bucket
	key    BucketKey
	index  int // index within bucket.tracked
}

// HashGrid is an incremental spatial hash for broad-phase collision detection
// and AoI queries. Entities are registered once and updated incrementally;
// only bucket-boundary crossings trigger rehashing. Transient entries (derived
// spatial data like body segments) are cleared each tick.
type HashGrid struct {
	bucketSize    float32
	invBucketSize float32
	buckets       map[BucketKey]*bucket
	tracked       map[ecs.Entity]*trackInfo
}

// NewHashGrid creates a new spatial hash grid with the given bucket size.
func NewHashGrid(bucketSize float32) *HashGrid {
	return &HashGrid{
		bucketSize:    bucketSize,
		invBucketSize: 1.0 / bucketSize,
		buckets:       make(map[BucketKey]*bucket, 256),
		tracked:       make(map[ecs.Entity]*trackInfo, 256),
	}
}

// Register adds a tracked entity to the grid. Panics if already registered.
func (g *HashGrid) Register(entry Entry) {
	if _, exists := g.tracked[entry.Entity]; exists {
		panic("spatial.HashGrid: entity already registered")
	}
	key := g.bucketKey(entry.X, entry.Y)
	c := g.getOrCreateBucket(key)
	c.tracked = append(c.tracked, entry)
	g.tracked[entry.Entity] = &trackInfo{
		bucket: c,
		key:    key,
		index:  len(c.tracked) - 1,
	}
}

// Update updates a tracked entity's spatial data. Rehashes only if the bucket
// changed. Returns true if the entity changed buckets.
func (g *HashGrid) Update(entry Entry) bool {
	info := g.tracked[entry.Entity]
	newKey := g.bucketKey(entry.X, entry.Y)

	if newKey == info.key {
		// Same bucket — overwrite in place.
		info.bucket.tracked[info.index] = entry
		return false
	}

	// Bucket changed — swap-delete from old bucket, append to new one.
	oldBkt := info.bucket
	last := len(oldBkt.tracked) - 1
	if info.index != last {
		oldBkt.tracked[info.index] = oldBkt.tracked[last]
		// Update the swapped entity's trackInfo.
		swapped := oldBkt.tracked[info.index].Entity
		g.tracked[swapped].index = info.index
	}
	oldBkt.tracked = oldBkt.tracked[:last]

	newBkt := g.getOrCreateBucket(newKey)
	newBkt.tracked = append(newBkt.tracked, entry)
	info.bucket = newBkt
	info.key = newKey
	info.index = len(newBkt.tracked) - 1
	return true
}

// Deregister removes a tracked entity from the grid. No-op if not registered.
func (g *HashGrid) Deregister(entity ecs.Entity) {
	info, ok := g.tracked[entity]
	if !ok {
		return
	}

	// Swap-delete from bucket.
	last := len(info.bucket.tracked) - 1
	if info.index != last {
		info.bucket.tracked[info.index] = info.bucket.tracked[last]
		swapped := info.bucket.tracked[info.index].Entity
		g.tracked[swapped].index = info.index
	}
	info.bucket.tracked = info.bucket.tracked[:last]
	delete(g.tracked, entity)
}

// IsRegistered returns whether an entity is tracked in the grid.
func (g *HashGrid) IsRegistered(entity ecs.Entity) bool {
	_, ok := g.tracked[entity]
	return ok
}

// InsertTransient adds an untracked entry that will be cleared by ClearTransient.
// Use for derived spatial data (e.g. body segment checkpoints) that is rebuilt each tick.
func (g *HashGrid) InsertTransient(entry Entry) {
	key := g.bucketKey(entry.X, entry.Y)
	c := g.getOrCreateBucket(key)
	c.transient = append(c.transient, entry)
}

// ClearTransient removes all transient entries. Tracked entries are untouched.
func (g *HashGrid) ClearTransient() {
	for _, c := range g.buckets {
		c.transient = c.transient[:0]
	}
}

// Reset clears everything: tracked entries, transient entries, and the tracking map.
func (g *HashGrid) Reset() {
	for k := range g.buckets {
		delete(g.buckets, k)
	}
	for k := range g.tracked {
		delete(g.tracked, k)
	}
}

// TrackedCount returns the number of registered entities.
func (g *HashGrid) TrackedCount() int {
	return len(g.tracked)
}

// QueryRadius returns every entry whose center lies within radius of center.
// Results are appended to the provided slice to avoid allocation.
//
// The test is a SPHERE. Buckets stay 2D columns (see bucketKey), so the Z term
// filters inside the bucket scan and never partitions it — which means the
// bucket sweep is unchanged and this can only NARROW the result set relative
// to the cylinder it replaces, never widen it.
//
// In a 2D profile every Z is zero, so dz is zero and the results are identical
// entry for entry. That is a property of the data rather than a branch: adding
// a zero term cannot change a sum of squares, which is never negative zero.
//
// The center is a Vec3 rather than three scalars deliberately. With four
// adjacent float32 parameters, QueryRadius(pos.X, pos.Y, spec.Radius, pos.Z)
// compiles silently — and there are nine call sites where that is exactly the
// mistake to expect.
func (g *HashGrid) QueryRadius(center component.Vec3, radius float32, results []Entry) []Entry {
	minX := int32(math.Floor(float64((center.X - radius) * g.invBucketSize)))
	maxX := int32(math.Floor(float64((center.X + radius) * g.invBucketSize)))
	minY := int32(math.Floor(float64((center.Y - radius) * g.invBucketSize)))
	maxY := int32(math.Floor(float64((center.Y + radius) * g.invBucketSize)))

	r2 := radius * radius

	for x := minX; x <= maxX; x++ {
		for y := minY; y <= maxY; y++ {
			c := g.buckets[BucketKey{x, y}]
			if c == nil {
				continue
			}
			// Indexed rather than ranged by value: Entry is 40 bytes since it
			// gained Z, and this loop runs over every entry in every bucket
			// the sweep touches, on the replication path, every tick.
			for i := range c.tracked {
				if e := &c.tracked[i]; inSphere(e, center, r2) {
					results = append(results, *e)
				}
			}
			for i := range c.transient {
				if e := &c.transient[i]; inSphere(e, center, r2) {
					results = append(results, *e)
				}
			}
		}
	}
	return results
}

// inSphere reports whether e's center is within sqrt(r2) of center.
//
// The dz term is APPENDED and the 2D sub-expression left textually intact:
// regrouping as dx*dx + (dy*dy + dz*dz) can change which multiply an
// FMA-fusing backend contracts, and the whole 2D-invariance argument rests on
// this arithmetic being the old arithmetic plus a zero.
func inSphere(e *Entry, center component.Vec3, r2 float32) bool {
	dx := e.X - center.X
	dy := e.Y - center.Y
	dz := e.Z - center.Z
	return dx*dx+dy*dy+dz*dz <= r2
}

// QueryCollisions finds all pairs of entries that overlap, filtered by the collision matrix.
// Calls the callback for each colliding pair. Searches both tracked and transient entries.
func (g *HashGrid) QueryCollisions(collisionMatrix map[uint8]uint8, fn func(a, b Entry)) {
	offsets := [4]BucketKey{{0, 0}, {1, 0}, {0, 1}, {1, 1}}

	for key, c := range g.buckets {
		all := g.allEntries(c)

		// Check pairs within the same bucket.
		for i := 0; i < len(all); i++ {
			for j := i + 1; j < len(all); j++ {
				checkCollision(all[i], all[j], collisionMatrix, fn)
			}
		}

		// Check against neighboring buckets.
		for _, off := range offsets[1:] {
			neighbor := g.buckets[BucketKey{key.X + off.X, key.Y + off.Y}]
			if neighbor == nil {
				continue
			}
			neighborAll := g.allEntries(neighbor)
			for _, a := range all {
				for _, b := range neighborAll {
					checkCollision(a, b, collisionMatrix, fn)
				}
			}
		}
	}
}

// allEntries returns all entries in a bucket (tracked + transient) as a single slice.
func (g *HashGrid) allEntries(c *bucket) []Entry {
	total := len(c.tracked) + len(c.transient)
	if total == 0 {
		return nil
	}
	if len(c.transient) == 0 {
		return c.tracked
	}
	if len(c.tracked) == 0 {
		return c.transient
	}
	// Need to combine — allocate on stack for small counts, heap otherwise.
	combined := make([]Entry, 0, total)
	combined = append(combined, c.tracked...)
	combined = append(combined, c.transient...)
	return combined
}

func (g *HashGrid) getOrCreateBucket(key BucketKey) *bucket {
	c := g.buckets[key]
	if c == nil {
		c = &bucket{}
		g.buckets[key] = c
	}
	return c
}

func checkCollision(a, b Entry, matrix map[uint8]uint8, fn func(a, b Entry)) {
	// Check if these layers should collide
	targetA, okA := matrix[a.Layer]
	targetB, okB := matrix[b.Layer]

	canCollide := (okA && targetA&b.Layer != 0) || (okB && targetB&a.Layer != 0)
	if !canCollide {
		return
	}

	// Broad-phase: bounding SPHERE check.
	//
	// Radius must therefore bound the shape in every axis the profile uses,
	// Depth included — see component.Collider.Radius. A box whose Radius is
	// its inscribed rather than circumscribed radius starts rejecting pairs
	// that genuinely overlap once this term is here.
	//
	// The narrow phase this gate feeds is still 2D OBB/SAT reading Yaw. That
	// is phase 4b, deliberately: this commit stops the gate claiming that
	// everything in a column is a candidate, it does not make the contact
	// test three-dimensional.
	//
	// dz appended, 2D sub-expression intact — see inSphere.
	dx := a.X - b.X
	dy := a.Y - b.Y
	dz := a.Z - b.Z
	dist2 := dx*dx + dy*dy + dz*dz
	maxDist := a.Radius + b.Radius
	if dist2 > maxDist*maxDist {
		return
	}

	// Narrow-phase: shape-aware check
	if a.Shape == component.ShapeSphere && b.Shape == component.ShapeSphere {
		// Already passed broad-phase circle-circle
		fn(a, b)
		return
	}

	if a.Shape == component.ShapeBox && b.Shape == component.ShapeSphere {
		if obbCircleCollision(a, b) {
			fn(a, b)
		}
		return
	}

	if a.Shape == component.ShapeSphere && b.Shape == component.ShapeBox {
		if obbCircleCollision(b, a) {
			fn(a, b)
		}
		return
	}

	// Both rects: OBB-OBB via SAT
	if obbOBBCollision(a, b) {
		fn(a, b)
	}
}

// obbCircleCollision tests an oriented bounding box against a circle.
func obbCircleCollision(rect, circle Entry) bool {
	// Transform circle center into rect's local space
	// Yaw() is an atan2 over the quaternion, so take it once. These planar
	// routines are replaced by the three-axis test in phase 4b; until then
	// they read the yaw out of the full orientation rather than a cached
	// scalar field, and calling it per trig term would double that cost.
	yaw := rect.Rot.Yaw()
	cos := float32(math.Cos(float64(-yaw)))
	sin := float32(math.Sin(float64(-yaw)))
	dx := circle.X - rect.X
	dy := circle.Y - rect.Y
	localX := dx*cos - dy*sin
	localY := dx*sin + dy*cos

	// Clamp to rect half-extents to find closest point
	halfW := rect.Width / 2
	halfH := rect.Height / 2
	closestX := clamp(localX, -halfW, halfW)
	closestY := clamp(localY, -halfH, halfH)

	// Check distance from closest point to circle center
	cdx := localX - closestX
	cdy := localY - closestY
	return cdx*cdx+cdy*cdy <= circle.Radius*circle.Radius
}

// obbOBBCollision tests two oriented bounding boxes using the Separating Axis Theorem.
func obbOBBCollision(a, b Entry) bool {
	// Get the 4 axes to test (2 per rect)
	// One Yaw() per entry: it is an atan2 over the quaternion. See
	// obbCircleCollision.
	yawA, yawB := a.Rot.Yaw(), b.Rot.Yaw()
	cosA := float32(math.Cos(float64(yawA)))
	sinA := float32(math.Sin(float64(yawA)))
	cosB := float32(math.Cos(float64(yawB)))
	sinB := float32(math.Sin(float64(yawB)))

	// Axes for rect A: forward (cosA, sinA) and right (−sinA, cosA)
	// Axes for rect B: forward (cosB, sinB) and right (−sinB, cosB)
	axes := [4][2]float32{
		{cosA, sinA},
		{-sinA, cosA},
		{cosB, sinB},
		{-sinB, cosB},
	}

	// Half-extents
	halfA := [2]float32{a.Width / 2, a.Height / 2}
	halfB := [2]float32{b.Width / 2, b.Height / 2}

	// Centers
	dx := b.X - a.X
	dy := b.Y - a.Y

	for _, axis := range axes {
		// Project center offset onto axis
		dist := abs32(dx*axis[0] + dy*axis[1])

		// Project half-extents of A onto axis
		projA := halfA[0]*abs32(cosA*axis[0]+sinA*axis[1]) +
			halfA[1]*abs32(-sinA*axis[0]+cosA*axis[1])

		// Project half-extents of B onto axis
		projB := halfB[0]*abs32(cosB*axis[0]+sinB*axis[1]) +
			halfB[1]*abs32(-sinB*axis[0]+cosB*axis[1])

		if dist > projA+projB {
			return false // Separating axis found
		}
	}

	return true // No separating axis — collision
}

func clamp(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func (g *HashGrid) bucketKey(x, y float32) BucketKey {
	return BucketKey{
		X: int32(math.Floor(float64(x * g.invBucketSize))),
		Y: int32(math.Floor(float64(y * g.invBucketSize))),
	}
}
