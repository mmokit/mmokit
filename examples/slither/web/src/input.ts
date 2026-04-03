import type { Network } from "./network";

export class Input {
  targetAngle: number = 0;
  boost: boolean = false;

  private mouseX: number = 0;
  private mouseY: number = 0;
  private canvas: HTMLCanvasElement | null = null;
  private network: Network;
  private inputInterval: number = 0;

  constructor(network: Network) {
    this.network = network;
  }

  attach(canvas: HTMLCanvasElement) {
    this.canvas = canvas;

    window.addEventListener("mousemove", (e) => {
      this.mouseX = e.clientX;
      this.mouseY = e.clientY;
      this.updateAngle();
    });

    window.addEventListener("mousedown", (e) => {
      if (e.button === 0) this.boost = true;
    });

    window.addEventListener("mouseup", (e) => {
      if (e.button === 0) this.boost = false;
    });

    window.addEventListener("keydown", (e) => {
      if (e.code === "Space") {
        e.preventDefault();
        this.boost = true;
      }
    });

    window.addEventListener("keyup", (e) => {
      if (e.code === "Space") {
        this.boost = false;
      }
    });
  }

  private updateAngle() {
    if (!this.canvas) return;
    // Use getBoundingClientRect for CSS pixel coords (matches clientX/clientY)
    const rect = this.canvas.getBoundingClientRect();
    const centerX = rect.left + rect.width / 2;
    const centerY = rect.top + rect.height / 2;
    this.targetAngle = Math.atan2(this.mouseY - centerY, this.mouseX - centerX);
  }

  startSending() {
    if (this.inputInterval) return;
    this.inputInterval = window.setInterval(() => {
      this.network.sendInput(this.targetAngle, this.boost);
    }, 50);
  }

  stopSending() {
    if (this.inputInterval) {
      clearInterval(this.inputInterval);
      this.inputInterval = 0;
    }
  }
}
