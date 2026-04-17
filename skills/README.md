# Gru skills

Claude Code skills shipped in the Gru repo. These are tracked in git so they
evolve alongside the code they document, but Claude Code looks for skills in
`.claude/skills/` (which is gitignored). To make these discoverable, symlink
the ones you want into place:

```bash
mkdir -p .claude/skills
ln -s ../../skills/gru .claude/skills/gru
```

Or copy them if you prefer:

```bash
cp -r skills/gru .claude/skills/gru
```

## Skills in this directory

| Skill | When to invoke |
|---|---|
| [`gru/scaffold-env`](./gru/scaffold-env/SKILL.md) | Setting up a new Gru environment spec, or auditing an existing project's env for Gru compatibility |
