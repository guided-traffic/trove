// @ts-check
const eslint = require('@eslint/js');
const { defineConfig, globalIgnores } = require('eslint/config');
const tseslint = require('typescript-eslint');
const angular = require('angular-eslint');

// R-UI-046 lint rules: no subscribe( in components, no NgZone, no bypassSecurityTrust*, no direct
// localStorage access outside the storage wrapper (src/app/core/storage.ts, added with the first
// use of browser storage).
const restrictedSyntax = [
  {
    selector: "CallExpression[callee.property.name='subscribe']",
    message: 'R-UI-046: no subscribe(). Convert with toSignal at the edge.',
  },
  {
    selector: "MemberExpression[property.name=/^bypassSecurityTrust/]",
    message: 'R-UI-046 / R-UI-001: bypassSecurityTrust* is banned.',
  },
  {
    selector: "Identifier[name=/^(localStorage|sessionStorage)$/]",
    message: 'R-UI-046: browser storage only through the storage wrapper.',
  },
];

module.exports = defineConfig([
  globalIgnores(['dist/**', '.angular/**', 'coverage/**', 'node_modules/**', 'src/app/api/generated/**']),
  {
    files: ['src/**/*.ts'],
    extends: [
      eslint.configs.recommended,
      tseslint.configs.recommended,
      tseslint.configs.stylistic,
      angular.configs.tsRecommended,
    ],
    processor: angular.processInlineTemplates,
    rules: {
      '@angular-eslint/directive-selector': [
        'error',
        { type: 'attribute', prefix: 'trove', style: 'camelCase' },
      ],
      '@angular-eslint/component-selector': [
        'error',
        { type: 'element', prefix: 'trove', style: 'kebab-case' },
      ],
      'no-restricted-syntax': ['error', ...restrictedSyntax],
      'no-restricted-imports': [
        'error',
        {
          paths: [
            { name: '@angular/core', importNames: ['NgZone'], message: 'R-UI-046: the app is zoneless.' },
          ],
        },
      ],
    },
  },
  {
    files: ['src/app/core/storage.ts'],
    rules: { 'no-restricted-syntax': 'off' },
  },
  {
    files: ['src/**/*.html'],
    extends: [angular.configs.templateRecommended, angular.configs.templateAccessibility],
    rules: {},
  },
  {
    files: ['scripts/**/*.mjs', 'eslint.config.js'],
    extends: [eslint.configs.recommended],
    languageOptions: { globals: { process: 'readonly', URL: 'readonly', console: 'readonly', require: 'readonly', module: 'readonly' } },
  },
]);
