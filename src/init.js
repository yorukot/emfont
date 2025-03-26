import fs from "fs";
import path from "path";
import { promisify } from "util";
import { db } from "./database.js"; // 匯入 db.js 中的資料庫連線模組

const readdir = promisify(fs.readdir);
const stat = promisify(fs.stat);
//init check

// 讀取並執行 SQL 腳本檔案
async function executeSQLFile(filePath) {
    const sql = await fs.promises.readFile(filePath, 'utf-8');
    try {
      await db.query(sql);
      console.log(`Executed SQL script from ${filePath}`);
    } catch (err) {
      console.error(`Error executing SQL script from ${filePath}:`, err);
    }
  }

//check database
async function insertFontTypes() {
    const fontsDir = path.resolve("src/static/fonts");
    try {
      // 取得 `fontsDir` 下的所有子項目
      const items = await readdir(fontsDir);
      
      // 篩選出僅包含資料夾的名稱
      const fontNames = [];
      for (const item of items) {
        const itemPath = path.join(fontsDir, item);
        const stats = await stat(itemPath);
        if (stats.isDirectory()) {
          fontNames.push(item);
        }
      }
  
      if (fontNames.length === 0) {
        console.log("No font directories found.");
        return;
      }
      
      // 插入字型名稱（避免重複）
      try {
        for (const fontName of fontNames) {
          await db.query(
            `INSERT INTO font_types (font_name) VALUES ($1)
             ON CONFLICT (font_name) DO NOTHING;`,
            [fontName]
          );
        }
        console.log("Font types inserted successfully.");
      } finally {
      }
    } catch (error) {
      console.error("Error inserting font types:", error);
    }
  }

async function initCheck()
{
    try
    {
        const schemaFilePath = path.resolve("src/static/sql/schema.sql");
        await executeSQLFile(schemaFilePath);
        await insertFontTypes();
        console.log("init success");
        return true;
    }
    catch(err)
    {
        console.error("Error init break:", err);
        return false;
    }
}
export{initCheck}