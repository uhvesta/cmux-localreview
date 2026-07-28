import { randomUUID } from "node:crypto";
import type { Database } from "bun:sqlite";

export interface AskQuestion {
  id: number;
  questionSetId: string;
  body: string;
  position: number;
  createdAt: number;
  updatedAt: number;
}

export interface AskQuestionSet {
  id: string;
  name: string;
  questions: AskQuestion[];
  createdAt: number;
  updatedAt: number;
}

interface QuestionSetRow {
  id: string;
  name: string;
  created_at: number;
  updated_at: number;
}

interface QuestionRow {
  id: number;
  question_set_id: string;
  body: string;
  position: number;
  created_at: number;
  updated_at: number;
}

function questionFromRow(row: QuestionRow): AskQuestion {
  return {
    id: row.id,
    questionSetId: row.question_set_id,
    body: row.body,
    position: row.position,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

function setFromRow(db: Database, row: QuestionSetRow): AskQuestionSet {
  const questions = db
    .query(`SELECT * FROM questions WHERE question_set_id = ? ORDER BY position, id`)
    .all(row.id) as QuestionRow[];
  return {
    id: row.id,
    name: row.name,
    questions: questions.map(questionFromRow),
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

export function getQuestionSet(db: Database, id: string): AskQuestionSet | undefined {
  const row = db.query(`SELECT * FROM question_sets WHERE id = ?`).get(id) as QuestionSetRow | null;
  return row ? setFromRow(db, row) : undefined;
}

export function listQuestionSets(db: Database): AskQuestionSet[] {
  const rows = db.query(`SELECT * FROM question_sets ORDER BY updated_at DESC, name COLLATE NOCASE`).all() as QuestionSetRow[];
  return rows.map((row) => setFromRow(db, row));
}

export function createQuestionSet(db: Database, input: { name: string; questions?: string[] }): AskQuestionSet {
  const now = Date.now();
  const id = randomUUID();
  db.transaction(() => {
    db.query(`INSERT INTO question_sets(id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`)
      .run(id, input.name, now, now);
    for (const [position, body] of (input.questions ?? []).entries()) {
      db.query(
        `INSERT INTO questions(question_set_id, body, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
      ).run(id, body, position, now, now);
    }
  })();
  return getQuestionSet(db, id)!;
}

/** Replaces the ordered questions atomically, preserving the set identity. */
export function updateQuestionSet(
  db: Database,
  id: string,
  input: { name?: string; questions?: string[] },
): AskQuestionSet | undefined {
  const current = getQuestionSet(db, id);
  if (!current) return undefined;
  const now = Date.now();
  db.transaction(() => {
    db.query(`UPDATE question_sets SET name = ?, updated_at = ? WHERE id = ?`)
      .run(input.name ?? current.name, now, id);
    if (input.questions) {
      db.query(`DELETE FROM questions WHERE question_set_id = ?`).run(id);
      for (const [position, body] of input.questions.entries()) {
        db.query(
          `INSERT INTO questions(question_set_id, body, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
        ).run(id, body, position, now, now);
      }
    }
  })();
  return getQuestionSet(db, id);
}

export function deleteQuestionSet(db: Database, id: string): boolean {
  const result = db.query(`DELETE FROM question_sets WHERE id = ?`).run(id);
  return Number(result.changes) > 0;
}
