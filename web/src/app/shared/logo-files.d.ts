// A logo file or the logos' notice imported with the `text` loader of the Angular builder
// (`import text from '…/NOTICE.txt' with { loader: 'text' }`) is its source as a string. Only format-icon.spec.ts does
// that; the application loads the logos as images from web/public/formats (Q-UI-226).
declare module '*.svg' {
  const text: string;
  export default text;
}

declare module '*.txt' {
  const text: string;
  export default text;
}
