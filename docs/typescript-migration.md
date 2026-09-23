# Frontend TypeScript migration

All three themes now use TypeScript source files, TypeScript 7, and Vite 8. Each `build` script runs `typecheck` before bundling.

The previous JavaScript code has many type mismatches with current React and UI libraries. The migration keeps those files explicit with `// @ts-nocheck` while the remaining source is checked. At this stage, the exclusions are 18 files in `default`, 24 in `air`, and 76 in `berry`.

Remove each exclusion after fixing its actual type errors. Run `npm run typecheck` and `npm run build` in that theme after each removal. Do not add new exclusions for new code.

The `default` and `air` themes stay on React 18 because `semantic-ui-react` 2.1.5 declares support through React 18. The `berry` theme uses React 19.
