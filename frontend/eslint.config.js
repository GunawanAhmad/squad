import js from '@eslint/js';
import tseslint from 'typescript-eslint';
import vue from 'eslint-plugin-vue';
import globals from 'globals';

export default [
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...vue.configs['flat/recommended'],
  {
    languageOptions: { globals: globals.browser, parserOptions: { parser: tseslint.parser } }, // TS inside <script lang="ts">
    rules: { 'no-empty': ['error', { allowEmptyCatch: true }] }, // empty catches are deliberate fallbacks
  },
  { files: ['vite.config.js', 'eslint.config.js'], languageOptions: { globals: globals.node } },
];
