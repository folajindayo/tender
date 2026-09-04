/**
 * Capture helpers.
 *
 * A full-resolution phone photo is 8-12MB and will stall on venue wifi, so
 * frames are downscaled before upload. Quality stays high enough for serial
 * numbers to remain legible.
 */

const MAX_EDGE = 1600;
const QUALITY = 0.82;

/** Draws a video frame to a canvas, downscaled, and returns it as a JPEG. */
export async function captureFrame(video: HTMLVideoElement): Promise<Blob> {
  const { videoWidth: w, videoHeight: h } = video;
  if (!w || !h) throw new Error("The camera is not ready yet.");
  return encode((canvas) => {
    const { width, height } = fit(w, h);
    canvas.width = width;
    canvas.height = height;
    canvas.getContext("2d")!.drawImage(video, 0, 0, width, height);
  });
}

/** Downscales a file chosen from the photo roll. */
export async function shrinkFile(file: File): Promise<Blob> {
  const bitmap = await createImageBitmap(file);
  try {
    return await encode((canvas) => {
      const { width, height } = fit(bitmap.width, bitmap.height);
      canvas.width = width;
      canvas.height = height;
      canvas.getContext("2d")!.drawImage(bitmap, 0, 0, width, height);
    });
  } finally {
    bitmap.close();
  }
}

function fit(w: number, h: number): { width: number; height: number } {
  const scale = Math.min(1, MAX_EDGE / Math.max(w, h));
  return { width: Math.round(w * scale), height: Math.round(h * scale) };
}

function encode(draw: (canvas: HTMLCanvasElement) => void): Promise<Blob> {
  const canvas = document.createElement("canvas");
  draw(canvas);
  return new Promise((resolve, reject) => {
    canvas.toBlob(
      (blob) => (blob ? resolve(blob) : reject(new Error("Could not read the photo."))),
      "image/jpeg",
      QUALITY,
    );
  });
}

/**
 * Opens the rear camera. Rejects with a readable message on the failures that
 * actually happen in the field: no permission, no camera, or an insecure origin.
 */
export async function openRearCamera(): Promise<MediaStream> {
  if (!window.isSecureContext) {
    throw new Error(
      "The camera needs a secure connection. Open this page over HTTPS.",
    );
  }
  if (!navigator.mediaDevices?.getUserMedia) {
    throw new Error("This browser cannot open the camera. Use the photo roll instead.");
  }
  try {
    return await navigator.mediaDevices.getUserMedia({
      video: { facingMode: { ideal: "environment" }, width: { ideal: 1920 } },
      audio: false,
    });
  } catch (err) {
    const name = (err as DOMException)?.name;
    if (name === "NotAllowedError") {
      throw new Error("Camera access was declined. Allow it, or use the photo roll.");
    }
    if (name === "NotFoundError") {
      throw new Error("No camera found on this device. Use the photo roll instead.");
    }
    throw new Error("Could not open the camera. Use the photo roll instead.");
  }
}
