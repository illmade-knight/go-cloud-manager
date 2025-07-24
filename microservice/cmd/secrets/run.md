export YOUR_EMAIL=$(gcloud config get-value account)
export PROJECT_ID=$(gcloud config get-value project)

# Grant your user account the Secret Manager Admin role for the project
gcloud projects add-iam-policy-binding $PROJECT_ID \
--member="user:${YOUR_EMAIL}" \
--role="roles/secretmanager.admin"

go run . <project-id> SECRET_NAME1=VALUE1 SECRET_NAME2=VALUE2 ...