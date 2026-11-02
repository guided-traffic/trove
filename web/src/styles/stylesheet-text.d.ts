// A stylesheet imported with the `text` loader of the Angular builder
// (`import text from './main.scss' with { loader: 'text' }`) is its source as a string. Only the
// palette test does that (palette.spec.ts); the application never imports a stylesheet.
declare module '*.scss' {
  const text: string;
  export default text;
}
