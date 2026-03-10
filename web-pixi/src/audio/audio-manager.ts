import { Howl, Howler } from "howler";
import { SoundId, SOUND_DEFS } from "./sounds";

const LS_KEY_SFX = "audio_sfx_vol";
const LS_KEY_MUSIC = "audio_music_vol";
const LS_KEY_MASTER = "audio_master_vol";

export class AudioManager {
  private sounds = new Map<SoundId, Howl>();
  private music: Howl | null = null;
  private musicPlaying = false;

  private _sfxVolume: number;
  private _musicVolume: number;
  private _masterVolume: number;

  // Track looping sound IDs (howler play IDs) so we can stop them
  private activeLoops = new Map<SoundId, number>();

  constructor() {
    this._sfxVolume = this.loadFloat(LS_KEY_SFX, 0.7);
    this._musicVolume = this.loadFloat(LS_KEY_MUSIC, 0.3);
    this._masterVolume = this.loadFloat(LS_KEY_MASTER, 1.0);
  }

  init(): void {
    Howler.volume(this._masterVolume);

    for (const def of SOUND_DEFS) {
      if (def.id === SoundId.AmbientMusic) {
        this.music = new Howl({
          src: [def.src],
          loop: true,
          volume: 0, // will fade in
          preload: true,
        });
      } else {
        const howl = new Howl({
          src: [def.src],
          volume: def.volume * this._sfxVolume,
          loop: def.loop ?? false,
          preload: true,
        });
        this.sounds.set(def.id, howl);
      }
    }
  }

  /** Play a one-shot sound effect */
  play(id: SoundId): void {
    const howl = this.sounds.get(id);
    if (howl) howl.play();
  }

  /** Play a sound and stop it after a duration (ms) with a short fade-out */
  playFor(id: SoundId, durationMs: number): void {
    const howl = this.sounds.get(id);
    if (!howl) return;
    const playId = howl.play();
    setTimeout(() => {
      howl.fade(howl.volume(), 0, 200, playId);
      setTimeout(() => howl.stop(playId), 200);
    }, durationMs);
  }

  /** Start a looping sound (e.g., mining laser) */
  loop(id: SoundId): void {
    if (this.activeLoops.has(id)) return; // already looping
    const howl = this.sounds.get(id);
    if (howl) {
      const playId = howl.play();
      this.activeLoops.set(id, playId);
    }
  }

  /** Stop a looping sound */
  stopLoop(id: SoundId): void {
    const playId = this.activeLoops.get(id);
    if (playId !== undefined) {
      const howl = this.sounds.get(id);
      if (howl) howl.stop(playId);
      this.activeLoops.delete(id);
    }
  }

  /** Stop all active loops (e.g., on death/disconnect) */
  stopAllLoops(): void {
    for (const [id] of this.activeLoops) {
      this.stopLoop(id);
    }
  }

  /** Play a sound with stereo panning based on world position */
  playAt(
    id: SoundId,
    worldX: number,
    worldY: number,
    cameraX: number,
    cameraY: number,
    screenWidth: number,
  ): void {
    const howl = this.sounds.get(id);
    if (!howl) return;

    const dx = worldX - cameraX;
    const dy = worldY - cameraY;
    const pan = Math.max(-1, Math.min(1, dx / (screenWidth / 2)));
    const dist = Math.sqrt(dx * dx + dy * dy);
    const maxDist = screenWidth * 1.5;
    const distVolume = Math.max(0, 1 - dist / maxDist);

    if (distVolume <= 0) return; // too far away, skip

    const playId = howl.play();
    howl.stereo(pan, playId);
    howl.volume(howl.volume() * distVolume, playId);
  }

  playMusic(): void {
    if (!this.music || this.musicPlaying) return;
    this.musicPlaying = true;
    this.music.play();
    this.music.fade(0, this._musicVolume, 2000);
  }

  stopMusic(): void {
    if (!this.music || !this.musicPlaying) return;
    this.musicPlaying = false;
    this.music.fade(this._musicVolume, 0, 1000);
    setTimeout(() => {
      if (!this.musicPlaying) this.music?.stop();
    }, 1000);
  }

  // Volume getters
  get sfxVolume(): number { return this._sfxVolume; }
  get musicVolume(): number { return this._musicVolume; }
  get masterVolume(): number { return this._masterVolume; }

  setMasterVolume(v: number): void {
    this._masterVolume = v;
    Howler.volume(v);
    localStorage.setItem(LS_KEY_MASTER, String(v));
  }

  setSfxVolume(v: number): void {
    this._sfxVolume = v;
    // Update all loaded SFX volumes
    for (const def of SOUND_DEFS) {
      if (def.id === SoundId.AmbientMusic) continue;
      const howl = this.sounds.get(def.id);
      if (howl) howl.volume(def.volume * v);
    }
    localStorage.setItem(LS_KEY_SFX, String(v));
  }

  setMusicVolume(v: number): void {
    this._musicVolume = v;
    if (this.music && this.musicPlaying) {
      this.music.volume(v);
    }
    localStorage.setItem(LS_KEY_MUSIC, String(v));
  }

  private loadFloat(key: string, fallback: number): number {
    const stored = localStorage.getItem(key);
    if (stored === null) return fallback;
    const val = parseFloat(stored);
    return isNaN(val) ? fallback : val;
  }
}

/** Singleton instance */
export const audio = new AudioManager();
