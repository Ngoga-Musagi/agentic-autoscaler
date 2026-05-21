#!/usr/bin/env node
// Skill evaluation engine for agentic-autoscaler
// Reads prompt from stdin, outputs feedback JSON with skill suggestions

const fs = require('fs');
const path = require('path');

const rulesPath = path.join(__dirname, 'skill-rules.json');
const rules = JSON.parse(fs.readFileSync(rulesPath, 'utf8'));

let prompt = '';
process.stdin.on('data', d => (prompt += d));
process.stdin.on('end', () => {
  const lower = prompt.toLowerCase();
  const matches = [];

  for (const [skillName, rule] of Object.entries(rules)) {
    let score = 0;
    const reasons = [];

    for (const kw of rule.triggers.keywords || []) {
      if (lower.includes(kw)) {
        score += 2;
        reasons.push(`keyword "${kw}"`);
      }
    }
    for (const pat of rule.triggers.keywordPatterns || []) {
      if (new RegExp(pat, 'i').test(prompt)) {
        score += 3;
        reasons.push(`pattern /${pat}/`);
      }
    }
    for (const intent of rule.triggers.intentPatterns || []) {
      if (new RegExp(intent, 'i').test(prompt)) {
        score += 4;
        reasons.push(`intent /${intent}/`);
      }
    }

    const excluded = (rule.excludePatterns || []).some(p =>
      new RegExp(p, 'i').test(prompt)
    );
    if (excluded) score = 0;

    if (score >= rule.threshold) {
      matches.push({ skillName, score, reasons, path: rule.path });
    }
  }

  if (matches.length === 0) process.exit(0);

  matches.sort((a, b) => b.score - a.score);

  const lines = ['SKILL SUGGESTION — read these before proceeding:', ''];
  for (const m of matches) {
    const confidence = m.score >= 8 ? 'HIGH' : m.score >= 5 ? 'MEDIUM' : 'LOW';
    lines.push(`  [${confidence}] ${m.skillName}  →  @${m.path}`);
    lines.push(`         matched: ${m.reasons.join(', ')}`);
  }

  const feedback = { feedback: lines.join('\n') };
  process.stdout.write(JSON.stringify(feedback) + '\n');
});
