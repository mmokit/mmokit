export interface SegmentPos {
  x: number;
  y: number;
}

export interface SnakeData {
  id: number;
  prevHeadX: number;
  prevHeadY: number;
  currHeadX: number;
  currHeadY: number;
  renderHeadX: number;
  renderHeadY: number;
  angle: number;
  speed: number;
  mass: number;
  skinID: number;
  boosting: boolean;
  name: string;
  length: number;
  prevSegments: SegmentPos[];
  currSegments: SegmentPos[];
  renderSegments?: SegmentPos[];
}

export interface FoodData {
  id: number;
  x: number;
  y: number;
  value: number;
  color: number;
}

export interface LeaderEntry {
  name: string;
  mass: number;
  skinID: number;
}

export interface KillFeedEntry {
  victim: string;
  killer: string;
  mass: number;
  time: number;
}

export const CELL_SIZE = 8192;

export interface EatenFoodAnim {
  x: number;
  y: number;
  value: number;
  color: number;
  startTime: number;
}

export class GameState {
  snakes: Map<number, SnakeData> = new Map();
  food: Map<number, FoodData> = new Map();
  eatenFood: EatenFoodAnim[] = [];
  myEntityID: number = 0;

  originCellX: number = 0;
  originCellY: number = 0;

  leaderboard: LeaderEntry[] = [];
  killFeed: KillFeedEntry[] = [];
  lastTickTime: number = 0;
  tick: number = 0;
  spawned: boolean = false;
  dead: boolean = false;

  get cellOffsetX(): number {
    return this.originCellX * CELL_SIZE;
  }
  get cellOffsetY(): number {
    return this.originCellY * CELL_SIZE;
  }

  /**
   * Called on Spawned message. Rebases all entity positions from old cell
   * local space to new cell local space, keeping everything visually in place.
   */
  handleCellChange(cellX: number, cellY: number) {
    if (cellX === this.originCellX && cellY === this.originCellY) {
      return;
    }

    // Delta to convert old-local positions to new-local positions
    const dx = (this.originCellX - cellX) * CELL_SIZE;
    const dy = (this.originCellY - cellY) * CELL_SIZE;

    this.originCellX = cellX;
    this.originCellY = cellY;

    // Rebase all entities in place — no visual change, just coordinate frame shift
    for (const snake of this.snakes.values()) {
      snake.prevHeadX += dx;
      snake.prevHeadY += dy;
      snake.currHeadX += dx;
      snake.currHeadY += dy;
      snake.renderHeadX += dx;
      snake.renderHeadY += dy;
      for (const seg of snake.prevSegments) {
        seg.x += dx;
        seg.y += dy;
      }
      for (const seg of snake.currSegments) {
        seg.x += dx;
        seg.y += dy;
      }
      if (snake.renderSegments) {
        for (const seg of snake.renderSegments) {
          seg.x += dx;
          seg.y += dy;
        }
      }
    }
    for (const f of this.food.values()) {
      f.x += dx;
      f.y += dy;
    }
  }

  applyWorldUpdate(
    tick: number,
    snakes: {
      id: number;
      headX: number;
      headY: number;
      angle: number;
      speed: number;
      mass: number;
      skinID: number;
      length: number;
      boosting: boolean;
      name: string;
      segments: SegmentPos[];
    }[],
    foods: {
      id: number;
      x: number;
      y: number;
      value: number;
      color: number;
    }[],
    removed: number[]
  ) {
    this.tick = tick;
    this.lastTickTime = performance.now();

    const now = performance.now();
    for (const id of removed) {
      this.snakes.delete(id);
      const food = this.food.get(id);
      if (food) {
        this.eatenFood.push({
          x: food.x,
          y: food.y,
          value: food.value,
          color: food.color,
          startTime: now,
        });
        this.food.delete(id);
      }
    }
    // Expire old eat animations
    this.eatenFood = this.eatenFood.filter((e) => now - e.startTime < 300);

    for (const s of snakes) {
      const existing = this.snakes.get(s.id);
      if (existing) {
        existing.prevHeadX = existing.currHeadX;
        existing.prevHeadY = existing.currHeadY;
        existing.currHeadX = s.headX;
        existing.currHeadY = s.headY;
        existing.angle = s.angle;
        existing.speed = s.speed;
        existing.mass = s.mass;
        existing.skinID = s.skinID;
        existing.boosting = s.boosting;
        existing.name = s.name;
        existing.length = s.length;
        existing.prevSegments = existing.currSegments;
        existing.currSegments = s.segments;
      } else {
        this.snakes.set(s.id, {
          id: s.id,
          prevHeadX: s.headX,
          prevHeadY: s.headY,
          currHeadX: s.headX,
          currHeadY: s.headY,
          renderHeadX: s.headX,
          renderHeadY: s.headY,
          angle: s.angle,
          speed: s.speed,
          mass: s.mass,
          skinID: s.skinID,
          boosting: s.boosting,
          name: s.name,
          length: s.length,
          prevSegments: s.segments,
          currSegments: s.segments,
        });
      }
    }

    for (const f of foods) {
      this.food.set(f.id, {
        id: f.id,
        x: f.x,
        y: f.y,
        value: f.value,
        color: f.color,
      });
    }

    if (
      this.spawned &&
      this.myEntityID !== 0 &&
      !this.snakes.has(this.myEntityID)
    ) {
      this.dead = true;
    }
  }

  getMySnake(): SnakeData | undefined {
    return this.snakes.get(this.myEntityID);
  }
}
