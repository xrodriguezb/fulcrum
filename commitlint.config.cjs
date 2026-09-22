// Conventional commits, enforced at commit time rather than at review time.
// The type list is the one the build specification allows, no more.
module.exports = {
  extends: ['@commitlint/config-conventional'],
  // Automated dependency commits are written by a bot that cannot be asked to
  // wrap its body, and it lists every bumped version on one line. Without this
  // exception the hundred column rule blocks every dependency update forever,
  // which turns a style rule into a supply chain decision. The rule still
  // applies to everything a person writes.
  ignores: [
    (message) =>
      /^build\(deps(-dev)?\)(\(.*\))?: bump /.test(message) ||
      /^Signed-off-by: dependabot\[bot\]/m.test(message),
  ],
  rules: {
    'type-enum': [
      2,
      'always',
      ['feat', 'fix', 'test', 'refactor', 'docs', 'chore', 'ci', 'perf', 'build', 'security'],
    ],
    'subject-case': [2, 'always', 'lower-case'],
    'subject-full-stop': [2, 'never', '.'],
    'header-max-length': [2, 'always', 72],
    'body-max-line-length': [2, 'always', 100],
  },
};
