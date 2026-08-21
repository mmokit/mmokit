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
  isViewer: boolean;
}

export class Scene3D {
  private renderer: THREE.WebGLRenderer;
  private scene = new THREE.Scene();
  private camera: THREE.PerspectiveCamera;
  private meshes = new Map<number, THREE.Mesh>();
  private cubeGeom = new THREE.BoxGeometry(1, 1, 1);
  private cubeMat = new THREE.MeshLambertMaterial({ color: 0x6fa8dc });
  private viewerMat = new THREE.MeshLambertMaterial({ color: 0xe06666 });

  constructor(canvas: HTMLCanvasElement) {
    this.renderer = new THREE.WebGLRenderer({ canvas, antialias: true });
    this.renderer.setClearColor(0x11131a);

    this.camera = new THREE.PerspectiveCamera(70, 1, 1, 20000);

    this.scene.add(new THREE.AmbientLight(0xffffff, 0.55));
    const sun = new THREE.DirectionalLight(0xffffff, 1.1);
    sun.position.set(0.4, 0.7, 1);
    this.scene.add(sun);

    // A ground plane at z=0, which is where MoveWalk clamps. Without it
    // gravity is invisible: falling cubes would have nothing to fall towards.
    const grid = new THREE.GridHelper(4000, 40, 0x445, 0x223);
    grid.rotation.x = Math.PI / 2; // GridHelper is XZ by default; we are XY.
    this.scene.add(grid);

    this.resize(canvas.clientWidth || 1, canvas.clientHeight || 1);
  }

  resize(width: number, height: number): void {
    this.renderer.setSize(width, height, false);
    this.camera.aspect = width / Math.max(1, height);
    this.camera.updateProjectionMatrix();
  }

  /** Place the camera at a world position looking along its yaw and pitch. */
  setCamera(x: number, y: number, z: number, look: Look): void {
    this.camera.position.set(x, y, z);
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
        mesh = new THREE.Mesh(this.cubeGeom, e.isViewer ? this.viewerMat : this.cubeMat);
        this.scene.add(mesh);
        this.meshes.set(e.netID, mesh);
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
    }
  }

  render(): void {
    this.renderer.render(this.scene, this.camera);
  }
}
