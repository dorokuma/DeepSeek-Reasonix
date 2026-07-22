import { describe, expect, it } from "vitest";
import type { PackageRow, RegistryUser } from "../types";
import { PublishSchema } from "../lib/validation";
import { PackageRepo } from "./packages";

const now = "2026-07-22T00:00:00.000Z";
const user: RegistryUser = {
  id: 7,
  handle: "publisher",
  role: "member",
  emailVerified: true,
};

const existing: PackageRow = {
  id: 42,
  kind: "mcp",
  scope_handle: "publisher",
  name: "devkit",
  slug: "publisher/devkit",
  summary: "old",
  description: "",
  source: "https://github.com/o/r",
  install_kind: "auto",
  homepage: "",
  repo_url: "https://github.com/o/r",
  tags: "tool",
  latest_version: "2.7.0",
  status: "pending",
  verified: 0,
  publisher_id: 7,
  install_count: 0,
  star_count: 0,
  created_at: now,
  updated_at: now,
};

function fakePackageDB(reads: PackageRow[]) {
  const updates: { sql: string; values: unknown[] }[] = [];
  let packageReads = 0;
  const db = {
    prepare(sql: string) {
      let values: unknown[] = [];
      const statement = {
        bind(...bound: unknown[]) {
          values = bound;
          return statement;
        },
        async first<T>() {
          if (sql.startsWith("SELECT * FROM packages")) {
            const row = reads[Math.min(packageReads, reads.length - 1)];
            packageReads += 1;
            return row as T;
          }
          return null;
        },
        async run() {
          if (sql.startsWith("UPDATE packages SET")) updates.push({ sql, values });
          return { meta: { changes: 1 } };
        },
      };
      return statement;
    },
  };
  return { db: db as unknown as D1Database, updates };
}

function pluginInput() {
  return PublishSchema.parse({
    kind: "plugin",
    installKind: "plugin",
    name: "devkit",
    source: "https://github.com/o/r",
    repoUrl: "https://github.com/o/r",
    version: "2.7.1",
  });
}

describe("PackageRepo.publish", () => {
  it("persists a kind change when an owned pending package is republished as a plugin", async () => {
    const updated: PackageRow = { ...existing, kind: "plugin", install_kind: "plugin", latest_version: "2.7.1" };
    const { db, updates } = fakePackageDB([existing, updated]);
    const result = await new PackageRepo(db).publish(user, pluginInput(), now);

    expect(result.created).toBe(false);
    expect(result.row.kind).toBe("plugin");
    expect(updates).toHaveLength(1);
    expect(updates[0].sql).toContain("SET kind = ?1");
    expect(updates[0].values[0]).toBe("plugin");
    expect(updates[0].values[4]).toBe("plugin");
    expect(updates[0].values[10]).toBe("pending");
    expect(updates[0].values[11]).toBe(0);
    expect(updates[0].values[12]).toBe(existing.id);
  });

  it("returns an active verified package to review when its kind changes", async () => {
    const active: PackageRow = { ...existing, status: "active", verified: 1 };
    const requeued: PackageRow = {
      ...active,
      kind: "plugin",
      install_kind: "plugin",
      latest_version: "2.7.1",
      status: "pending",
      verified: 0,
    };
    const { db, updates } = fakePackageDB([active, requeued]);

    const result = await new PackageRepo(db).publish(user, pluginInput(), now);

    expect(result.row.status).toBe("pending");
    expect(result.row.verified).toBe(0);
    expect(updates[0].values[10]).toBe("pending");
    expect(updates[0].values[11]).toBe(0);
  });

  it("keeps ordinary updates to an active package live and verified", async () => {
    const active: PackageRow = { ...existing, status: "active", verified: 1 };
    const updated: PackageRow = { ...active, install_kind: "mcp", latest_version: "2.7.1" };
    const { db, updates } = fakePackageDB([active, updated]);
    const input = PublishSchema.parse({
      kind: "mcp",
      name: "devkit",
      source: "https://github.com/o/r",
      repoUrl: "https://github.com/o/r",
      version: "2.7.1",
    });

    const result = await new PackageRepo(db).publish(user, input, now);

    expect(result.row.status).toBe("active");
    expect(result.row.verified).toBe(1);
    expect(updates[0].values[4]).toBe("mcp");
    expect(updates[0].values[10]).toBe("active");
    expect(updates[0].values[11]).toBe(1);
  });

  it("clears verification when a non-active package changes kind", async () => {
    const hidden: PackageRow = { ...existing, status: "hidden", verified: 1 };
    const updated: PackageRow = {
      ...hidden,
      kind: "plugin",
      install_kind: "plugin",
      latest_version: "2.7.1",
      verified: 0,
    };
    const { db, updates } = fakePackageDB([hidden, updated]);

    const result = await new PackageRepo(db).publish(user, pluginInput(), now);

    expect(result.row.status).toBe("hidden");
    expect(result.row.verified).toBe(0);
    expect(updates[0].values[10]).toBe("hidden");
    expect(updates[0].values[11]).toBe(0);
  });
});
