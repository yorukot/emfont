import fs from "fs";
import path from "path";
import { S3Client, ListObjectsV2Command, GetObjectCommand, ListBucketsCommand } from "@aws-sdk/client-s3";
import { promisify } from "util";
async function listAllObjects(client, bucket, prefix) {
    let allObjects = [];
    let continuationToken;
    do {
        const response = await client.send(
            new ListObjectsV2Command({
                Bucket: bucket,
                Prefix: prefix,
                ContinuationToken: continuationToken
            })
        );
        if (response.Contents) {
            allObjects = allObjects.concat(response.Contents);
        }
        continuationToken = response.IsTruncated ? response.NextContinuationToken : undefined;
    } while (continuationToken);
    return allObjects;
}
export default async state => {
    const bucketName = process.env.MINIO_BUCKET;
    if (!bucketName) {
        console.log("⏭️  沒有設定 MINIO_BUCKET 環境變數，跳過下載字型");
        return;
    }
    const LOCAL_MINIO_CLIENT = new S3Client({
        region: "auto",
        endpoint: process.env.MINIO_ENDPOINT,
        credentials: {
            accessKeyId: process.env.MINIO_USERNAME,
            secretAccessKey: process.env.MINIO_PASSWORD
        },
        forcePathStyle: true
    });

    try {
        const data = await LOCAL_MINIO_CLIENT.send(new ListBucketsCommand({}));
        console.log(
            "🗃️  MinIO 連接成功，找到的 Bucket:",
            data.Buckets.map(b => b.Name)
        );
        state.local = false;
    } catch (err) {
        console.error("❌ 無法連接到 MinIO，跳過字型下載", err);
        return;
    }

    try {
        console.log("🛒 正在取得 MinIO 內的檔案清單")
        const listResponse = await listAllObjects(LOCAL_MINIO_CLIENT, bucketName, "original-fonts");
        const listGenerated = await listAllObjects(LOCAL_MINIO_CLIENT, bucketName, "_generated")

        console.log(`🔄 找到 ${listResponse.Contents.length} 個原始字體，${listGenerated.Contents.length} 個分割好的，開始下載...`);
        await Promise.all(
            [...listResponse.Contents, ...listGenerated.Contents].map(async file => {
                const fileKey = file.Key;
                if (!fileKey) return;
                const localPath = path.join("src", "_data", fileKey);

                // check if file already exists
                const localFileExists = fs.existsSync(localPath);
                if (localFileExists) {
                    console.log(`✅ ${fileKey} 已經存在，跳過下載`);
                    return;
                }

                const getCommand = new GetObjectCommand({
                    Bucket: bucketName,
                    Key: fileKey
                });

                const data = await LOCAL_MINIO_CLIENT.send(getCommand);
                const localDir = path.dirname(localPath);
                fs.mkdirSync(localDir, { recursive: true });

                // **使用 Promise 等待檔案寫入完成**
                await new Promise((resolve, reject) => {
                    //Pipe the stream from S3 to the local file
                    const fileStream = fs.createWriteStream(localPath);
                    data.Body.pipe(fileStream);

                    fileStream.on("finish", () => {
                        resolve();
                    });

                    fileStream.on("error", err => {
                        console.error(`❌ 下載檔案失敗: ${fileKey}`, err);
                        reject(err);
                    });
                });
            })
        );

        console.log("✅ 所有字體下載完成");
    } catch (err) {
        console.error("❌ 下載檔案失敗:", err);
    }
    console.log("✅ 所有字體文件已成功下載");
};
