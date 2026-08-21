/**
 * The three.js scene.
 *
 * NO TEST IMPORTS THIS FILE, and that is a constraint rather than an
 * oversight: CI runs the frontend suites with no `bun install`
 * (.github/workflows/ci.yml verifies it by moving node_modules aside), so a
 * bare specifier reachable from a test would force an install step into a
 * required check. The assertable maths lives in flycontrol.ts, which imports
 * nothing.
 */
import * as THREE from "three";
import type { Quat } from "../sdk/_core/delta-decoder-core.js";
import { forwardVector, type Look } from "./flycontrol";
import { worldBounds, type CellRect, type EntityClass, type Topology } from "./topology";

/**
 * THIS IS LOAD-BEARING. mmokit is Z-up: gravity acts along -Z and the
 * quaternion the server sends is expressed in that frame. three.js defaults to
 * Y-up, and without this every object would be rotated a quarter turn — a
 * picture that looks plausible, animates smoothly, and is wrong, which no test
 * in this repository would catch.
 */
THREE.Object3D.DEFAULT_UP.set(0, 0, 1);

export interface Renderable {
  netID: number;
  worldX: number;
  worldY: number;
  worldZ: number;
  rot: Quat;
  /** Half-extent, from the collider. */
  size: number;
  /** How to colour it: your own entity, your cell's, or a neighbour's. */
  kind: EntityClass;
}

/** Blue = in your cell, amber = owned by another cell, red = you. */
const CLASS_COLOR: Record<EntityClass, number> = {
  self: 0xe06666,
  local: 0x6fa8dc,
  remote: 0xe8b44a,
  unknown: 0x8899a6,
};

export class Scene3D {
  private renderer: THREE.WebGLRenderer;
  private scene = new THREE.Scene();
  private camera: THREE.PerspectiveCamera;
  private meshes = new Map<number, THREE.Mesh>();
  private meshClass = new Map<number, EntityClass>();
  private cubeGeom = new THREE.BoxGeometry(1, 1, 1);
  private materials: Record<EntityClass, THREE.MeshLambertMaterial>;
  private cellLines: THREE.Object3D | null = null;
  private aoiRing: THREE.LineLoop | null = null;
  /** The topology overlay: cell boundaries, labels, and the AoI ring. */
  private debugVisible = true;
  private lastAoI = 0;

  constructor(canvas: HTMLCanvasElement) {
    this.renderer = new THREE.WebGLRenderer({ canvas, antialias: true });
    this.renderer.setClearColor(0x11131a);

    this.camera = new THREE.PerspectiveCamera(70, 1, 1, 20000);

    this.materials = {
      self: new THREE.MeshLambertMaterial({ color: CLASS_COLOR.self }),
      local: new THREE.MeshLambertMaterial({ color: CLASS_COLOR.local }),
      remote: new THREE.MeshLambertMaterial({ color: CLASS_COLOR.remote }),
      unknown: new THREE.MeshLambertMaterial({ color: CLASS_COLOR.unknown }),
    };

    this.scene.add(new THREE.AmbientLight(0xffffff, 0.55));
    const sun = new THREE.DirectionalLight(0xffffff, 1.1);
    sun.position.set(0.4, 0.7, 1);
    this.scene.add(sun);

    this.resize(canvas.clientWidth || 1, canvas.clientHeight || 1);
  }

  resize(width: number, height: number): void {
    this.renderer.setSize(width, height, false);
    this.camera.aspect = width / Math.max(1, height);
    this.camera.updateProjectionMatrix();
  }

  /**
   * Draw the ground plane from the SERVER's cell rectangles.
   *
   * It used to be a fixed 4000-unit GridHelper centred on the origin, while
   * the world occupies 0..gridW*cellSize — so every cell sat in one quadrant
   * and the other three implied world that was not there. Drawing the actual
   * cells fixes the extent, marks the boundaries an entity changes colour at,
   * and follows a split, because DebugInfo re-broadcasts after one.
   */
  setTopology(topo: Topology): void {
    if (this.cellLines) {
      this.scene.remove(this.cellLines);
      disposeTree(this.cellLines);
      this.cellLines = null;
    }
    if (topo.cells.length === 0) return;

    const group = new THREE.Group();
    const b = worldBounds(topo);

    // Cell boundaries, bright: these are the lines entities change colour at.
    const edges: number[] = [];
    for (const c of topo.cells) {
      const { originX: x, originY: y, size: s } = c;
      edges.push(x, y, 0, x + s, y, 0);
      edges.push(x + s, y, 0, x + s, y + s, 0);
      edges.push(x + s, y + s, 0, x, y + s, 0);
      edges.push(x, y + s, 0, x, y, 0);
    }
    const edgeGeom = new THREE.BufferGeometry();
    edgeGeom.setAttribute("position", new THREE.Float32BufferAttribute(edges, 3));
    group.add(new THREE.LineSegments(edgeGeom, new THREE.LineBasicMaterial({ color: 0x5b7fa6 })));

    // A dim fill grid across the world's real extent, for depth perception.
    const step = topo.baseCellSize > 0 ? topo.baseCellSize / 10 : 100;
    const fill: number[] = [];
    for (let x = b.minX; x <= b.maxX + 1e-6; x += step) {
      fill.push(x, b.minY, 0, x, b.maxY, 0);
    }
    for (let y = b.minY; y <= b.maxY + 1e-6; y += step) {
      fill.push(b.minX, y, 0, b.maxX, y, 0);
    }
    const fillGeom = new THREE.BufferGeometry();
    fillGeom.setAttribute("position", new THREE.Float32BufferAttribute(fill, 3));
    group.add(new THREE.LineSegments(fillGeom, new THREE.LineBasicMaterial({ color: 0x243140 })));

    // A label at each cell's centre, carrying the same coords the console
    // takes (`cell split 0_0`) and the node that owns it. In a distributed run
    // the node is the whole point: it is the only place a browser can see
    // which host is simulating what.
    for (const c of topo.cells) {
      group.add(this.cellLabel(c));
    }

    group.visible = this.debugVisible;
    this.cellLines = group;
    this.scene.add(group);
  }

  /**
   * Show or hide the topology overlay — boundaries, labels and the AoI ring.
   *
   * Toggled rather than rebuilt, so pressing the key twice does not cost four
   * canvas textures and a geometry rebuild.
   */
  setDebugVisible(visible: boolean): void {
    this.debugVisible = visible;
    if (this.cellLines) this.cellLines.visible = visible;
    if (this.aoiRing) this.aoiRing.visible = visible;
  }

  /**
   * Draw the area-of-interest radius as a ring on the ground under the viewer.
   *
   * This is the answer to "why did that cube vanish". Entities are replicated
   * to a client by distance, and without the radius drawn the boundary is
   * invisible — things simply appear and disappear partway to the horizon.
   */
  setAoIRadius(radius: number): void {
    if (radius === this.lastAoI) return;
    this.lastAoI = radius;
    if (this.aoiRing) {
      this.scene.remove(this.aoiRing);
      disposeTree(this.aoiRing);
      this.aoiRing = null;
    }
    if (radius <= 0) return;

    const segments = 128;
    const pts: number[] = [];
    for (let i = 0; i < segments; i++) {
      const a = (i / segments) * Math.PI * 2;
      pts.push(Math.cos(a) * radius, Math.sin(a) * radius, 0);
    }
    const geom = new THREE.BufferGeometry();
    geom.setAttribute("position", new THREE.Float32BufferAttribute(pts, 3));
    this.aoiRing = new THREE.LineLoop(geom, new THREE.LineBasicMaterial({ color: 0x4f7f63 }));
    this.aoiRing.visible = this.debugVisible;
    this.scene.add(this.aoiRing);
  }

  /** A billboarded cell label, drawn into a canvas texture. */
  private cellLabel(c: CellRect): THREE.Sprite {
    const coords = c.depth > 0 ? `d${c.depth}:${c.cellX},${c.cellY}` : `${c.cellX},${c.cellY}`;
    const canvas = document.createElement("canvas");
    canvas.width = 256;
    canvas.height = 128;
    const ctx = canvas.getContext("2d")!;
    ctx.fillStyle = "rgba(12,17,24,0.66)";
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.font = "bold 46px ui-monospace, monospace";
    ctx.fillStyle = "#9fc6ea";
    ctx.fillText(coords, 128, 44);
    ctx.font = "28px ui-monospace, monospace";
    ctx.fillStyle = "#6d8ba8";
    ctx.fillText(c.nodeID || "local", 128, 92);

    const sprite = new THREE.Sprite(
      new THREE.SpriteMaterial({ map: new THREE.CanvasTexture(canvas), transparent: true }),
    );
    // Just above the ground plane, so it is readable from above and does not
    // z-fight the grid it labels.
    sprite.position.set(c.originX + c.size / 2, c.originY + c.size / 2, 14);
    sprite.scale.set(c.size * 0.3, c.size * 0.15, 1);
    return sprite;
  }

  /** Place the camera at a world position looking along its yaw and pitch. */
  setCamera(x: number, y: number, z: number, look: Look): void {
    this.camera.position.set(x, y, z);
    // The AoI ring is centred on the VIEWER, not on a cell: replication
    // distance is measured from the viewer's own position.
    this.aoiRing?.position.set(x, y, 0.5);
    const f = forwardVector(look);
    this.camera.lookAt(x + f.x, y + f.y, z + f.z);
  }

  /**
   * Reconcile the scene with the current entity set. Meshes are keyed by
   * netID and reused; anything absent from `entities` is disposed, which is
   * what keeps a long session from leaking a mesh per despawn.
   */
  sync(entities: Renderable[]): void {
    const seen = new Set<number>();
    for (const e of entities) {
      seen.add(e.netID);
      let mesh = this.meshes.get(e.netID);
      if (!mesh) {
        mesh = new THREE.Mesh(this.cubeGeom, this.materials[e.kind]);
        this.scene.add(mesh);
        this.meshes.set(e.netID, mesh);
        this.meshClass.set(e.netID, e.kind);
      } else if (this.meshClass.get(e.netID) !== e.kind) {
        // Recolour in place when an entity crosses a cell line, which is the
        // moment the distinction is worth seeing.
        mesh.material = this.materials[e.kind];
        this.meshClass.set(e.netID, e.kind);
      }
      mesh.position.set(e.worldX, e.worldY, e.worldZ);
      // The whole point of the phase: orientation applied as a quaternion,
      // straight from the wire, with no Euler conversion to lose a degree of
      // freedom in.
      mesh.quaternion.set(e.rot.x, e.rot.y, e.rot.z, e.rot.w);
      mesh.scale.setScalar(e.size * 2);
    }
    for (const [netID, mesh] of this.meshes) {
      if (seen.has(netID)) continue;
      this.scene.remove(mesh);
      this.meshes.delete(netID);
      this.meshClass.delete(netID);
    }
  }

  render(): void {
    this.renderer.render(this.scene, this.camera);
  }
}

/**
 * Release the GPU resources under an object.
 *
 * The topology overlay is rebuilt on every DebugInfo push, and a split pushes
 * one — so without this a long session watching splits leaks a canvas texture
 * per cell per push, which is the one leak in this client big enough to
 * notice.
 */
function disposeTree(root: THREE.Object3D): void {
  root.traverse((obj) => {
    const any = obj as unknown as {
      geometry?: THREE.BufferGeometry;
      material?: THREE.Material & { map?: THREE.Texture | null };
    };
    any.geometry?.dispose();
    any.material?.map?.dispose();
    any.material?.dispose();
  });
}
