export enum SoundId {
  // Abilities (mapped to slots 0-5)
  MissileLaunch = "missile-launch",
  RailgunFire = "railgun-fire",
  IonBurn = "ion-burn",
  PlasmaTorpedo = "plasma-torpedo",
  ShieldActivate = "shield-activate",
  Afterburner = "afterburner",

  // World events
  Thruster = "thruster",
  MiningLaser = "mining-laser",
  Explosion = "explosion",
  HitImpact = "hit-impact",
  LootPickup = "loot-pickup",

  // UI
  TargetLock = "target-lock",
  UIClick = "ui-click",

  // Music
  AmbientMusic = "ambient-music",
}

/** Maps ability slot index to its sound */
export const ABILITY_SOUNDS: Record<number, SoundId> = {
  0: SoundId.MissileLaunch,
  1: SoundId.RailgunFire,
  2: SoundId.IonBurn,
  3: SoundId.PlasmaTorpedo,
  4: SoundId.ShieldActivate,
  5: SoundId.Afterburner,
};

export interface SoundDef {
  id: SoundId;
  src: string;
  volume: number;
  loop?: boolean;
}

export const SOUND_DEFS: SoundDef[] = [
  { id: SoundId.MissileLaunch, src: "/audio/sfx/missile-launch.ogg", volume: 0.5 },
  { id: SoundId.RailgunFire, src: "/audio/sfx/railgun-fire.ogg", volume: 0.6 },
  { id: SoundId.IonBurn, src: "/audio/sfx/ion-burn.ogg", volume: 0.5 },
  { id: SoundId.PlasmaTorpedo, src: "/audio/sfx/plasma-torpedo.ogg", volume: 0.7 },
  { id: SoundId.ShieldActivate, src: "/audio/sfx/shield-activate.ogg", volume: 0.6 },
  { id: SoundId.Afterburner, src: "/audio/sfx/afterburner.ogg", volume: 0.4 },
  { id: SoundId.Thruster, src: "/audio/sfx/thruster.ogg", volume: 0.08, loop: true },
  { id: SoundId.MiningLaser, src: "/audio/sfx/mining-laser.ogg", volume: 0.12, loop: true },
  { id: SoundId.Explosion, src: "/audio/sfx/explosion.ogg", volume: 0.7 },
  { id: SoundId.HitImpact, src: "/audio/sfx/hit-impact.ogg", volume: 0.5 },
  { id: SoundId.LootPickup, src: "/audio/sfx/loot-pickup.ogg", volume: 0.6 },
  { id: SoundId.TargetLock, src: "/audio/sfx/target-lock.ogg", volume: 0.4 },
  { id: SoundId.UIClick, src: "/audio/sfx/ui-click.ogg", volume: 0.4 },
  { id: SoundId.AmbientMusic, src: "/audio/music/space-ambient.ogg", volume: 1.0, loop: true },
];
