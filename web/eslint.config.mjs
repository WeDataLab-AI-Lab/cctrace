import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";

const eslintConfig = defineConfig([
  ...nextVitals,
  ...nextTs,
  // FRONT-RULE 자동 강제. Stage 1 리팩토링 완료(위반 0)로 error 승격 — 회귀 방지.
  // 동적 값 등 불가피한 inline style은 CSS 변수 + 사유 line-disable로 처리.
  {
    name: "front-rule",
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector: "CallExpression[callee.name='useMemo']",
          message:
            "FRONT-RULE: React Compiler가 메모이제이션을 자동 처리합니다. useMemo를 제거하세요.",
        },
        {
          selector: "CallExpression[callee.name='useCallback']",
          message:
            "FRONT-RULE: React Compiler가 메모이제이션을 자동 처리합니다. useCallback을 제거하세요.",
        },
        {
          selector: "JSXAttribute[name.name='style']",
          message:
            "FRONT-RULE: inline style 금지. Tailwind 클래스(필요 시 cn 유틸)를 사용하세요.",
        },
      ],
    },
  },
  // Override default ignores of eslint-config-next.
  globalIgnores([
    // Default ignores of eslint-config-next:
    ".next/**",
    "out/**",
    "build/**",
    "next-env.d.ts",
  ]),
]);

export default eslintConfig;
