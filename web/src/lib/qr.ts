import QRCode from "qrcode";

// toSVG renders text as an SVG string (lazy-loaded chunk).
export function toSVG(text: string): Promise<string> {
  return QRCode.toString(text, { type: "svg", margin: 1, errorCorrectionLevel: "M" });
}
